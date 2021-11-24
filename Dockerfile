FROM golang:1.17 as builder
WORKDIR /build
COPY go.mod .
COPY go.sum .
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o app
RUN chmod +x app

FROM alpine:latest

COPY wait-for-it.sh /usr/bin/wait-for-it
RUN chmod +x /usr/bin/wait-for-it

RUN apk --no-cache add tzdata bash && \
    cp /usr/share/zoneinfo/Europe/Minsk /etc/localtime && \
    echo "Europe/Minsk" >  /etc/timezone && \
    apk del tzdata && rm -rf /var/cache/apk/*
WORKDIR /app
COPY --from=builder /build/app /app/uploader
EXPOSE 7008

CMD /app/uploader
