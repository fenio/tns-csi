.PHONY: all build build-plugin clean deps install test test-unit test-scripts test-sanity test-all test-coverage \
	docker-build docker-push lint lint-fix lint-verbose mod-tidy-check check \
	test-e2e test-e2e-nfs test-e2e-nvmeof test-e2e-iscsi test-e2e-smb test-e2e-scale test-e2e-snapclone \
	changelog changelog-unreleased

DRIVER_NAME=tns-csi-driver
PLUGIN_NAME=kubectl-tns_csi
IMAGE_NAME=bfenski/tns-csi
REGISTRY?=docker.io

# Single source of truth for the golangci-lint version. CI workflows read this line
# (see .github/workflows/ci.yml and release.yml); keep .pre-commit-config.yaml in sync.
# Bump deliberately and fix any new warnings in the same PR.
GOLANGCI_LINT_VERSION ?= v2.14.0

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
	@echo "Running golangci-lint (CI pins $(GOLANGCI_LINT_VERSION))..."
	@$(GOLANGCI_LINT) version 2>/dev/null | grep -q "$(GOLANGCI_LINT_VERSION:v%=%)" || \
		echo "warning: local golangci-lint is not $(GOLANGCI_LINT_VERSION); results may differ from CI"
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

# Unit tests: race detector on, randomized order, driver + CLI packages (what CI runs).
test-unit:
	@echo "Running unit tests..."
	$(GOTEST) -v -short -race -shuffle=on ./pkg/... ./cmd/...

# Shell-script unit tests (test runners live in tests/sanity).
test-scripts:
	@echo "Running script tests..."
	bash tests/sanity/sanity-script_test.sh

# Fail if go.mod/go.sum are not tidy.
mod-tidy-check:
	@echo "Checking go.mod/go.sum are tidy..."
	$(GOMOD) tidy
	@git diff --exit-code -- go.mod go.sum || (echo "go.mod/go.sum not tidy: run 'go mod tidy'" && exit 1)

# Everything a PR must pass. CI runs exactly these targets.
check: lint mod-tidy-check test-unit test-scripts test-sanity build
	@echo "All checks passed"

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

test-e2e-smb:
	@echo "Running SMB E2E tests..."
	ginkgo -v --timeout=25m ./tests/e2e/smb/...

test-e2e-scale:
	@echo "Running Scale E2E tests (CSI operations with non-CSI noise data)..."
	ginkgo -v --timeout=30m ./tests/e2e/scale/...

test-e2e-snapclone:
	@echo "Running Snapshot/Clone Stress E2E tests..."
	ginkgo -v --timeout=120m ./tests/e2e/snapclone/...

# Changelog generation (requires git-cliff: cargo install git-cliff)
changelog:
	@echo "Generating changelog..."
	git-cliff -o CHANGELOG.md

changelog-unreleased:
	@echo "Generating unreleased changelog..."
	git-cliff --unreleased
