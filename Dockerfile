# syntax=docker/dockerfile:1.7

FROM golang:1.24-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -extldflags=-static" \
    -trimpath \
    -o /out/goop ./cmd/goop

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/goop /app/goop
COPY --from=builder /src/config.yml /app/config.yml
EXPOSE 8080
USER nonroot:nonroot
# Liveness/readiness should hit /healthz directly from your orchestrator.
ENTRYPOINT ["/app/goop", "-config=/app/config.yml"]
