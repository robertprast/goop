.PHONY: build build-debug run run-debug clean

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

