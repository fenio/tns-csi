# Integration Test Scripts

This directory contains **utility shell scripts** for specific testing scenarios. The main integration tests have been migrated to the Ginkgo E2E framework in `tests/e2e/`.

## Primary Test Location

**Main E2E tests are now in `tests/e2e/`** using Ginkgo/Gomega. See [tests/e2e/README.md](../e2e/README.md) for the primary testing documentation.

The GitHub Actions workflow (`.github/workflows/integration.yml`) runs Ginkgo E2E tests:
```bash
ginkgo -v --timeout=25m ./tests/e2e/nfs/...
ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...
ginkgo -v --timeout=25m ./tests/e2e/
```

## Scripts in This Directory

These shell scripts serve specific purposes outside the main E2E test suite:

### Active Scripts

| Script | Purpose | Used By |
|--------|---------|---------|
| `test-distro-basic.sh` | Tests basic functionality across Kubernetes distributions (k3s, k0s, minikube, etc.) | `distro-compatibility.yml` workflow |
| `test-pvc-lifecycle-nfs.sh` | Focused PVC lifecycle testing for NFS | `pvc-lifecycle.yml` workflow |
| `test-pvc-lifecycle-nvmeof.sh` | Focused PVC lifecycle testing for NVMe-oF | `pvc-lifecycle.yml` workflow |
| `test-pvc-lifecycle-nested-dataset.sh` | Tests nested dataset path handling | `pvc-lifecycle.yml` workflow |
| `test-orphaned-resources.sh` | Utility to detect orphaned TrueNAS resources | Manual cleanup operations |

### Directory Structure

```
tests/integration/
├── lib/
│   └── common.sh           # Shared bash library functions
├── manifests/              # Kubernetes manifests for shell script tests
│   ├── pvc-nfs.yaml
│   ├── pod-nfs.yaml
│   ├── pvc-nvmeof.yaml
│   ├── pod-nvmeof.yaml
│   └── ...
├── test-distro-basic.sh    # Distribution compatibility testing
├── test-pvc-lifecycle-*.sh # PVC lifecycle tests
├── test-orphaned-resources.sh
└── README.md
```

## When to Use These Scripts

Use shell scripts in this directory when:
- Testing Kubernetes distribution compatibility (uses `test-distro-basic.sh`)
- Running quick PVC lifecycle validation (uses `test-pvc-lifecycle-*.sh`)
- Detecting orphaned resources on TrueNAS (uses `test-orphaned-resources.sh`)

Use Ginkgo E2E tests (`tests/e2e/`) for:
- Comprehensive feature testing (snapshots, cloning, expansion, etc.)
- Protocol-specific testing (NFS, NVMe-oF)
- CI/CD integration tests
- New test development

## Common Library Functions

The `lib/common.sh` library provides reusable functions:

### Test Output Functions

- `test_step(step, total, description)` - Print a test step header
- `test_success(message)` - Print success message with checkmark
- `test_error(message)` - Print error message
- `test_warning(message)` - Print warning message
- `test_info(message)` - Print info message

### Test Workflow Functions

- `verify_cluster()` - Verify cluster accessibility
- `deploy_driver(protocol, [helm_args...])` - Deploy CSI driver for a protocol
- `wait_for_driver()` - Wait for CSI driver pods to be ready
- `create_pvc(manifest, pvc_name)` - Create and wait for PVC to bind
- `create_test_pod(manifest, pod_name)` - Create and wait for pod to be ready
- `test_io_operations(pod_name, path, test_type)` - Run I/O tests
- `cleanup_test(pod_name, pvc_name)` - Delete test resources
- `show_diagnostic_logs([pod_name], [pvc_name])` - Show logs on failure
- `test_summary(protocol, status)` - Print test summary

## Running Shell Script Tests

### Prerequisites

```bash
export TRUENAS_HOST="your-truenas-host.example.com"
export TRUENAS_API_KEY="your-api-key-here"
export TRUENAS_POOL="your-pool-name"
```

### Run Distribution Compatibility Test

```bash
./tests/integration/test-distro-basic.sh
```

### Run PVC Lifecycle Tests

```bash
./tests/integration/test-pvc-lifecycle-nfs.sh
./tests/integration/test-pvc-lifecycle-nvmeof.sh
```

### Check for Orphaned Resources

```bash
./tests/integration/test-orphaned-resources.sh
```

## Migration to Ginkgo

The main integration tests were migrated to Ginkgo for:

| Aspect | Bash Scripts | Ginkgo E2E Tests |
|--------|--------------|------------------|
| Location | `tests/integration/` | `tests/e2e/` |
| Framework | Custom bash library | Ginkgo/Gomega |
| Waiting | Fixed `sleep` + polling | `Eventually()` with backoff |
| Cleanup | Trap-based, error-prone | LIFO stack, guaranteed |
| Type Safety | None | Full Go type system |
| IDE Support | Limited | Full (autocomplete, refactoring) |
| Parallelism | Manual | Ginkgo-managed |
| CI Integration | Script exit codes | JUnit reports |

For new tests, always use the Ginkgo framework in `tests/e2e/`.

## Contributing

When adding new shell scripts to this directory:

1. Use the shared library (`source lib/common.sh`)
2. Follow the existing script patterns
3. Update this README with script purpose
4. Consider if a Ginkgo E2E test would be more appropriate
