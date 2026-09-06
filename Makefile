BINARY ?= harvester-scaleset
OUTPUT_DIR ?= bin
IMAGE ?= harvester-scaleset:dev
GO ?= go
CGO_ENABLED ?= 0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT_SHA ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commitSHA=$(COMMIT_SHA)

.PHONY: all build test docker-build

all: build

build:
	mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/$(BINARY) ./cmd/harvester-scaleset

test:
	$(GO) test ./...

docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT_SHA=$(COMMIT_SHA) \
		--tag $(IMAGE) \
		.
