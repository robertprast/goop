# Development stage with hot reloading
FROM golang:1.24-alpine AS development
WORKDIR /app
RUN apk add --no-cache git
RUN go install github.com/air-verse/air@latest
COPY go.mod go.sum ./
RUN go mod download
COPY . .
EXPOSE 8080
CMD ["air", "-c", ".air.toml"]

# Production builder stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY . .
RUN go mod tidy
RUN go build -ldflags "-s -w" -o bin/goop main.go

# Production stage
FROM alpine:latest AS production
WORKDIR /app
COPY --from=builder /app/bin/goop /app/goop
EXPOSE 8080
COPY config.yml /app/config.yml
ENTRYPOINT ["/app/goop"]