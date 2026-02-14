.PHONY: all build build-plugin clean test docker-build docker-push lint lint-fix test-coverage test-e2e test-e2e-nfs test-e2e-nvmeof test-e2e-iscsi test-e2e-scale changelog

DRIVER_NAME=tns-csi-driver
PLUGIN_NAME=kubectl-tns_csi
IMAGE_NAME=bfenski/tns-csi
REGISTRY?=docker.io

# Version information - derived from git tags
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOLANGCI_LINT=golangci-lint

# Build parameters with version injection
LDFLAGS=-ldflags "-s -w \
	-X main.version=$(VERSION) \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.buildDate=$(BUILD_DATE)"
BUILD_DIR=bin

all: build

build:
	@echo "Building $(DRIVER_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(DRIVER_NAME) ./cmd/tns-csi-driver

build-plugin:
	@echo "Building $(PLUGIN_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(GIT_COMMIT)" \
		-o $(BUILD_DIR)/$(PLUGIN_NAME) ./cmd/kubectl-tns-csi
	@echo "Plugin built: $(BUILD_DIR)/$(PLUGIN_NAME)"
	@echo "Install with: cp $(BUILD_DIR)/$(PLUGIN_NAME) /usr/local/bin/"

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
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE_NAME):$(VERSION) .
	docker tag $(IMAGE_NAME):$(VERSION) $(IMAGE_NAME):latest

docker-push:
	@echo "Pushing Docker image $(IMAGE_NAME):$(VERSION)..."
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

install:
	@echo "Installing $(DRIVER_NAME)..."
	$(GOBUILD) $(LDFLAGS) -o $(GOPATH)/bin/$(DRIVER_NAME) ./cmd/tns-csi-driver

# Sanity tests
test-sanity:
	@echo "Running CSI sanity tests..."
	./tests/sanity/test-sanity.sh

test-unit:
	@echo "Running unit tests..."
	$(GOTEST) -v -short ./pkg/...

test-coverage:
	@echo "Running tests with coverage (for SonarQube)..."
	$(GOTEST) -v -short -coverprofile=coverage.out -covermode=atomic ./pkg/...
	$(GOTEST) -v -short -json ./pkg/... > test-report.json || true
	@echo "Coverage report: coverage.out"
	@echo "Test report: test-report.json"

test-all: test-unit test-sanity
	@echo "All tests completed"

# E2E tests (requires Ginkgo CLI and TrueNAS connection)
test-e2e:
	@echo "Running all E2E tests..."
	ginkgo -v --timeout=60m ./tests/e2e/...

test-e2e-nfs:
	@echo "Running NFS E2E tests..."
	ginkgo -v --timeout=25m ./tests/e2e/nfs/...

test-e2e-nvmeof:
	@echo "Running NVMe-oF E2E tests..."
	ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...

test-e2e-iscsi:
	@echo "Running iSCSI E2E tests..."
	ginkgo -v --timeout=40m ./tests/e2e/iscsi/...

test-e2e-scale:
	@echo "Running Scale E2E tests (CSI operations with non-CSI noise data)..."
	ginkgo -v --timeout=30m ./tests/e2e/scale/...

# Changelog generation (requires git-cliff: cargo install git-cliff)
changelog:
	@echo "Generating changelog..."
	git-cliff -o CHANGELOG.md

changelog-unreleased:
	@echo "Generating unreleased changelog..."
	git-cliff --unreleased
