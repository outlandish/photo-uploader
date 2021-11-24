package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-redis/cache/v8"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v4"
	"github.com/streadway/amqp"
)

var hmacSecret = []byte(os.Getenv("JWT_SECRET_KEY"))

func handleError(w http.ResponseWriter, err error, statusCode int) {
	http.Error(w, err.Error(), statusCode)
}

func getUploadDir(origin string, key string) string {
	return fmt.Sprintf(
		"%s/%s/%s",
		"./uploads/"+os.Getenv("APP_ENV")+"/",
		origin,
		key,
	)
}

func validateToken(w http.ResponseWriter, r *http.Request) error {
	tokenString := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	// Parse takes the token string and a function for looking up the key. The latter is especially
	// useful if you use multiple keys for your application.  The standard is to use 'kid' in the
	// head of the token to identify which key to use, but the parsed token (head and claims) is provided
	// to the callback, providing flexibility.
	_, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// hmacSampleSecret is a []byte containing your secret, e.g. []byte("my_secret_key")
		return hmacSecret, nil
	})

	return err
}

func sendMessageToS3Uploader(conn *amqp.Connection, body string) {
	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	err = ch.Publish(
		"",
		"upload_s3",
		false,
		false,
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        []byte(body),
		},
	)

	if err != nil {
		log.Println(err.Error())
	}
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "vigbo-gallery-redis:6379",
		Password: "", // no password set
		DB:       2,
	})
	mycache := cache.New(&cache.Options{Redis: rdb})
	ctx := context.TODO()

	conn, err := amqp.Dial(
		fmt.Sprintf(
			"amqp://%s:%s@%s:5672/",
			os.Getenv("RABBITMQ_USER"),
			os.Getenv("RABBITMQ_PASS"),
			"vigbo-gallery-rabbit",
		),
	)

	failOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	http.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		err := validateToken(w, r)

		if err != nil {
			handleError(w, err, http.StatusUnauthorized)
			return
		}

		key := r.FormValue("key")
		origin := r.FormValue("origin")
		fileName := r.FormValue("fileName")

		if len(key) == 0 || len(origin) == 0 || len(fileName) == 0 {
			handleError(w, errors.New("required fields are not provided"), http.StatusBadRequest)
			return
		}

		err = r.ParseMultipartForm(1 << 20)

		if err != nil {
			handleError(w, err, http.StatusInternalServerError)
			return
		}

		// The argument to FormFile must match the file input on the frontend
		file, _, err := r.FormFile("file")

		if err != nil {
			handleError(w, err, http.StatusBadRequest)
			return
		}

		defer file.Close()

		// Create the uploads folder if it doesn't already exist
		uploadDir := getUploadDir(origin, key)
		err = os.MkdirAll(uploadDir, os.ModePerm)

		if err != nil {
			handleError(w, err, http.StatusInternalServerError)
			return
		}

		// Create a new file in the uploads directory
		dst, err := os.Create(uploadDir + "/" + fileName)

		if err != nil {
			handleError(w, err, http.StatusInternalServerError)
			return
		}

		defer dst.Close()

		// Copy the uploaded file to the filesystem at the specified destination
		_, err = io.Copy(dst, file)

		if err != nil {
			handleError(w, err, http.StatusInternalServerError)
			return
		}

		photoPath := key + "/" + fileName

		if os.Getenv("LINK_SERVICE_MODE") != "gae" {
			if err := mycache.Set(&cache.Item{
				Ctx:   ctx,
				Key:   photoPath,
				Value: "1",
				TTL:   time.Minute * 5,
			}); err != nil {
				handleError(w, err, http.StatusInternalServerError)
				return
			}
		}

		sendMessageToS3Uploader(conn, photoPath)

		w.Write([]byte("Uploaded successfully"))
	})

	log.Println("Listening on localhost:7008")
	log.Fatal(http.ListenAndServe(":7008", nil))
}
