# Contributing to TNS CSI Driver

Thank you for your interest in contributing to the TNS CSI Driver project! This document provides guidelines and instructions for contributing.

## Code of Conduct

This project aims to foster an open and welcoming environment. Please be respectful and professional in all interactions.

## How Can I Contribute?

### Reporting Bugs

Before creating bug reports, please check existing issues to avoid duplicates. When creating a bug report, include:

- **Clear title and description** - Explain the problem clearly
- **Steps to reproduce** - Provide detailed steps to reproduce the issue
- **Expected behavior** - What you expected to happen
- **Actual behavior** - What actually happened
- **Environment details**:
  - Kubernetes version
  - TrueNAS version
  - CSI driver version
  - Storage protocol (NFS/NVMe-oF)
- **Logs** - Include relevant logs from controller and node pods
- **Configuration** - Share relevant StorageClass and PVC configurations (sanitize secrets!)

### Suggesting Enhancements

Enhancement suggestions are welcome! Please:

- Use a clear and descriptive title
- Provide detailed description of the proposed functionality
- Explain why this enhancement would be useful
- Include examples of how it would work

### Pull Requests

1. **Fork the repository** and create your branch from `main`
2. **Follow the coding standards** (see below)
3. **Add tests** for your changes when applicable
4. **Update documentation** if needed
5. **Ensure tests pass** - Run `make test` and `make lint`
6. **Write clear commit messages** (see commit guidelines below)
7. **Submit a pull request** with a clear description

## Development Setup

### Prerequisites

- Go 1.21 or later
- Docker (for building images)
- Kubernetes cluster (Kind, k3s, or full cluster)
- golangci-lint for code linting

### Clone and Build

```bash
# Clone your fork
git clone https://github.com/yourusername/tns-csi.git
cd tns-csi

# Install dependencies
make deps

# Build the driver
make build

# Run tests
make test

# Run linter
make lint
```

### Running Locally

See [docs/KIND.md](docs/KIND.md) for instructions on setting up a local development environment with Kind.

## Coding Standards

### Go Code Style

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting (integrated in `make lint-fix`)
- Run `golangci-lint` before committing
- Keep functions small and focused
- Write clear, descriptive variable names
- Add comments for exported functions and complex logic

### Project Structure

```
tns-csi/
├── cmd/                    # Main applications
│   └── tns-csi-driver/    # Driver entry point
├── pkg/                    # Library code
│   ├── driver/            # CSI driver implementation
│   └── tnsapi/            # TrueNAS API client
├── tests/                  # Test files
│   ├── e2e/               # End-to-end tests
│   └── integration/       # Integration tests
├── deploy/                 # Kubernetes manifests
├── charts/                 # Helm charts
└── docs/                   # Documentation
```

### Important Guidelines

1. **DO NOT modify WebSocket connection logic** in `pkg/tnsapi/client.go` without proven need
   - The ping/pong system is working correctly
   - Connection handling has been thoroughly tested

2. **Add comprehensive error handling**
   - Always check errors
   - Provide context with wrapped errors
   - Log errors at appropriate levels

3. **Write testable code**
   - Keep business logic separate from I/O
   - Use interfaces for dependencies
   - Write unit tests for new functionality

## Testing

### Unit Tests

```bash
# Run all tests
make test

# Run specific package tests
go test -v ./pkg/driver/

# Run with coverage
go test -cover ./...
```

### Ginkgo E2E Tests

Integration tests use [Ginkgo](https://onsi.github.io/ginkgo/) and run automatically in CI using self-hosted runners. To run locally:

```bash
# Install Ginkgo CLI
go install github.com/onsi/ginkgo/v2/ginkgo@latest

# Set required environment variables
export TRUENAS_HOST="your-truenas-ip"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"

# Run NFS E2E tests
ginkgo -v --timeout=25m ./tests/e2e/nfs/...

# Run NVMe-oF E2E tests
ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...

# Run all E2E tests
ginkgo -v --timeout=60m ./tests/e2e/...

# Run specific test by name
ginkgo -v --focus="expand" ./tests/e2e/nfs/...
```

See [tests/e2e/README.md](tests/e2e/README.md) for detailed E2E test documentation.

### Linting

```bash
# Check code style
make lint

# Auto-fix issues where possible
make lint-fix
```

## Commit Message Guidelines

Write clear, meaningful commit messages:

```
Short summary (50 chars or less)

More detailed explanation if needed. Wrap at 72 characters.
Explain the problem this commit solves and why you chose this approach.

- Bullet points are fine
- Use present tense: "Add feature" not "Added feature"
- Reference issues: "Fixes #123" or "Related to #456"
```

### Commit Types

- **feat**: New feature
- **fix**: Bug fix
- **docs**: Documentation changes
- **test**: Adding or updating tests
- **refactor**: Code refactoring
- **chore**: Maintenance tasks

Examples:
```
feat: Add support for volume expansion in NVMe-oF

fix: Correct NFS mount options for better performance

docs: Update quickstart guide with TrueNAS 24.04 specifics

test: Add integration tests for NVMe-oF volume lifecycle
```

## Pull Request Process

1. **Update documentation** - Ensure docs reflect your changes
2. **Add tests** - New features need test coverage
3. **Pass CI checks** - All tests and lints must pass
4. **Get review** - At least one maintainer approval required
5. **Squash commits** - Keep history clean (we'll squash on merge)

### PR Description Template

```markdown
## Description
Brief description of changes

## Motivation
Why is this change needed?

## Changes
- List key changes
- Be specific

## Testing
How was this tested?

## Checklist
- [ ] Tests added/updated
- [ ] Documentation updated
- [ ] Linting passes
- [ ] Commits follow guidelines
```

## Questions?

Feel free to open an issue for questions or discussion. We're here to help!

## License

By contributing, you agree that your contributions will be licensed under the GPL-3.0 License.
