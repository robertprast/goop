.PHONY: build build-debug run run-debug clean dev-setup dev-start dev-stop dev-bootstrap

GO_FILES = $(shell find . -name '*.go')
BIN_NAME := $(shell basename $(CURDIR))
BIN= ./bin/$(BIN_NAME)
DOCKER_REPO=$(BIN_NAME)
DOCKER_TAG=dev
DOCKER_REGISTRY=
MAIN=./main.go

ARGS=

$(BIN): $(GO_FILES)
	@go build -ldflags "-s -w" -o $(BIN) $(MAIN)

build: $(BIN)

build-debug: $(GO_FILES)
	@go build -gcflags "all=-N -l" -o $(BIN) $(MAIN)

build-docker: $(GO_FILES)
	@docker build -t $(DOCKER_REGISTRY)$(DOCKER_REPO):$(DOCKER_TAG) .

run: build
	@./$(BIN) $(ARGS)

run-debug: build-debug
	@./$(BIN) $(ARGS)

run-docker: build-docker
	@docker run --rm -p 8080:8080 -it $(DOCKER_REGISTRY)$(DOCKER_REPO):$(DOCKER_TAG) $(ARGS)

# Development commands
dev-setup:
	@echo "Setting up development environment..."
	@docker compose up -d postgres
	@echo "Waiting for PostgreSQL to be ready..."
	@sleep 5
	@echo "✅ PostgreSQL is ready!"
	@echo "Set your environment variables:"
	@echo "  export DATABASE_URL='postgres://goop_user:goop_password@localhost:5432/goop?sslmode=disable'"
	@echo "  export GOOP_DISABLE_AUTH=true"

dev-start:
	@echo "Starting development services..."
	@docker compose up -d

dev-stop:
	@echo "Stopping development services..."
	@docker compose down

dev-clean:
	@echo "Cleaning development environment (removes all data)..."
	@docker compose down -v
	@docker system prune -f

dev-run: dev-setup
	@echo "Starting Goop with development settings..."
	@export DATABASE_URL='postgres://goop_user:goop_password@localhost:5432/goop?sslmode=disable' && \
	 export PORT=8080 && \
	 go run main.go

sam-build:
	@echo "Building SAM application and Lambda container image..."
	@sam build --use-container

sam-local: sam-build
	@echo "Starting local SAM API..."
	@sam local start-api  

sam-deploy: sam-build
	@echo "Deploying SAM application to AWS..."
	@sam deploy --guided

clean:
	@echo "Cleaning up..."
	@rm -f $(BIN)
	@rm -rf .aws-sam