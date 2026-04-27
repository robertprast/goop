.PHONY: build run test fmt vet lint tidy docker docker-run clean

BIN     := bin/goop
PKG     := ./cmd/goop
GOFLAGS := -trimpath -ldflags="-s -w"

$(BIN): $(shell find . -name '*.go' -not -path './bin/*')
	@mkdir -p bin
	go build $(GOFLAGS) -o $(BIN) $(PKG)

build: $(BIN)

run: build
	./$(BIN) -config=config.yml

test:
	go test ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null || { echo "install golangci-lint: https://golangci-lint.run"; exit 1; }
	golangci-lint run

tidy:
	go mod tidy

docker:
	docker compose build

docker-run:
	docker compose up

clean:
	rm -rf bin
