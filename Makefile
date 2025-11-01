.PHONY: all build clean test docker-build docker-push lint lint-fix

DRIVER_NAME=tns-csi-driver
IMAGE_NAME=truenas/csi-driver
VERSION?=v0.1.0
REGISTRY?=docker.io

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOLANGCI_LINT=golangci-lint

# Build parameters
LDFLAGS=-ldflags "-s -w"
BUILD_DIR=bin

all: build

build:
	@echo "Building $(DRIVER_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(DRIVER_NAME) ./cmd/tns-csi-driver

clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)

test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

lint:
	@echo "Running golangci-lint..."
	$(GOLANGCI_LINT) run --config .golangci.yml ./...

lint-fix:
	@echo "Running golangci-lint with auto-fix..."
	$(GOLANGCI_LINT) run --config .golangci.yml --fix ./...

lint-verbose:
	@echo "Running golangci-lint (verbose)..."
	$(GOLANGCI_LINT) run --config .golangci.yml -v ./...

deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

docker-build:
	@echo "Building Docker image $(IMAGE_NAME):$(VERSION)..."
	docker build -t $(IMAGE_NAME):$(VERSION) .
	docker tag $(IMAGE_NAME):$(VERSION) $(IMAGE_NAME):latest

docker-push:
	@echo "Pushing Docker image $(IMAGE_NAME):$(VERSION)..."
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

install:
	@echo "Installing $(DRIVER_NAME)..."
	$(GOBUILD) $(LDFLAGS) -o $(GOPATH)/bin/$(DRIVER_NAME) ./cmd/tns-csi-driver
