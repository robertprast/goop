# Use the official Golang image as the base image
FROM golang:1.23-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY . .
RUN go mod tidy
RUN go build -ldflags "-s -w" -o bin/goop main.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/bin/goop /app/goop
EXPOSE 8080
COPY config.yaml /app/config.yaml
ENTRYPOINT ["/app/goop"]