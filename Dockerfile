# syntax=docker/dockerfile:1.7
#
# Two-stage build that cross-compiles a fully static goop binary, then assembles
# a `scratch`-based image containing exactly: the binary, ca-certs, /etc/passwd
# for a non-root UID, and config.yml. No shell, no package manager, no libc.

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build \
      -ldflags="-s -w -extldflags=-static" \
      -trimpath \
      -buildvcs=false \
      -o /out/goop ./cmd/goop

# Synthesize minimal /etc/passwd + /etc/group entries so USER 65532 resolves
# inside the scratch image (matches the distroless `nonroot` UID convention).
RUN printf "nonroot:x:65532:65532:nonroot:/:\n"  > /out/passwd \
 && printf "nonroot:x:65532:\n"                  > /out/group

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/passwd /etc/passwd
COPY --from=builder /out/group  /etc/group
COPY --from=builder /out/goop   /goop
COPY --from=builder /src/config.yml /config.yml

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/goop", "-config=/config.yml"]
