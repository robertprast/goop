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
	@docker compose build goop

run: build
	@./$(BIN) $(ARGS)

run-debug: build-debug
	@./$(BIN) $(ARGS)

dev-setup:
	@echo "Setting up development environment with Postgres and PGAdmin..."
	@docker compose up -d postgres pgadmin -d

dev-start-full-stack: dev-setup
	@echo "Starting development services..."
	@docker compose up goop

dev-stop:
	@echo "Stopping development services..."
	@docker compose down

dev-clean:
	@echo "Cleaning development environment (removes all data)..."
	@docker compose down -v
	@docker system prune -f

clean:
	@echo "Cleaning up..."
	@rm -f $(BIN)