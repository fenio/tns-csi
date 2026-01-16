# Testing Guide

## Overview

The TNS CSI Driver is tested comprehensively using **real infrastructure** - not mocks, simulators, or virtual TrueNAS instances. Every commit triggers automated tests against actual hardware and software.

## Testing Infrastructure

### Real Hardware, Real Tests

**Self-hosted GitHub Actions Runner:**
- Dedicated Linux server running GitHub Actions runner
- Hosted on: Akamai/Linode cloud infrastructure
- Runs real k3s Kubernetes clusters for each test
- No Kind clusters, no mocks - actual Kubernetes distribution

**Real TrueNAS Scale Server:**
- Physical TrueNAS Scale 25.10+ installation
- Real storage pools with ZFS
- Actual NFS shares and NVMe-oF subsystems
- Real network I/O and protocol operations

**Real Protocol Testing:**
- NFS: Actual NFS mounts from TrueNAS to Kubernetes nodes
- NVMe-oF: Real NVMe-oF TCP connections and block device operations
- WebSocket: Live API connections to TrueNAS with authentication
- Full end-to-end data path testing

### Why Real Infrastructure?

Testing against real infrastructure catches issues that mocks cannot:
- Network timing and race conditions
- Actual protocol behavior and error modes
- TrueNAS API quirks and edge cases
- Real-world performance characteristics
- Connection resilience and recovery
- Cleanup and resource management

## Test Framework

All integration tests use [Ginkgo](https://onsi.github.io/ginkgo/) v2 and [Gomega](https://onsi.github.io/gomega/) for BDD-style testing with:
- Structured `Describe`/`It` blocks for clear test organization
- `Eventually` for robust async waiting (no brittle `sleep` calls)
- LIFO cleanup stack ensuring resources are always cleaned up
- Parallel test execution support
- JUnit report generation for CI integration

## Automated Test Suite

### CSI Specification Compliance

**Sanity Tests:**
- Uses [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) v5.4.0
- Validates full CSI specification compliance
- Tests all CSI RPC calls and error conditions
- Location: `tests/sanity/`
- Run on: Every CI build

### Ginkgo E2E Integration Tests

Every push to main triggers comprehensive integration tests organized into three test suites:

#### NFS Test Suite (`tests/e2e/nfs/`)

| Test File | Description |
|-----------|-------------|
| `basic_test.go` | Volume provisioning, mounting, I/O operations, deletion |
| `access_modes_test.go` | RWO/RWX access mode validation |
| `adoption_test.go` | Volume adoption for GitOps workflows |
| `clone_test.go` | Volume cloning from snapshots |
| `concurrent_test.go` | 5 simultaneous volume creations |
| `delete_strategy_retain_test.go` | Volume retention on PVC deletion |
| `detached_snapshot_test.go` | Snapshot lifecycle without attached pods |
| `persistence_test.go` | Data survives pod restarts |
| `statefulset_test.go` | StatefulSet with VolumeClaimTemplates |
| `zfsprops_test.go` | Custom ZFS properties via StorageClass |

#### NVMe-oF Test Suite (`tests/e2e/nvmeof/`)

| Test File | Description |
|-----------|-------------|
| `basic_test.go` | Volume provisioning, mounting, I/O operations, deletion |
| `access_modes_test.go` | RWO access mode validation |
| `block_mode_test.go` | Raw block device mode testing |
| `clone_test.go` | Volume cloning from snapshots |
| `concurrent_test.go` | 5 simultaneous volume creations |
| `delete_strategy_retain_test.go` | Volume retention on PVC deletion |
| `detached_snapshot_test.go` | Snapshot lifecycle without attached pods |
| `persistence_test.go` | Data survives pod restarts |
| `statefulset_test.go` | StatefulSet with VolumeClaimTemplates |
| `zfsprops_test.go` | Custom ZFS properties via StorageClass |

#### Shared Test Suite (`tests/e2e/`)

| Test File | Description |
|-----------|-------------|
| `snapshot_restore_test.go` | Snapshot creation and restoration (both protocols) |
| `stress_test.go` | Volume stress testing |
| `name_templating_test.go` | Custom volume naming templates |
| `error_handling_test.go` | Error condition handling |
| `dual_mount_test.go` | Simultaneous NFS + NVMe-oF mounting |
| `connection_resilience_test.go` | WebSocket reconnection testing |

### Test Execution

Tests run via GitHub Actions workflow (`.github/workflows/integration.yml`):

```bash
# NFS tests (~25 minutes)
ginkgo -v --timeout=25m ./tests/e2e/nfs/...

# NVMe-oF tests (~40 minutes)
ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...

# Shared tests (~25 minutes)
ginkgo -v --timeout=25m ./tests/e2e/
```

Each test run:
- Deploys CSI driver via Helm to a fresh namespace
- Creates test resources (PVCs, pods, snapshots)
- Validates operations with `Eventually` for robustness
- Cleans up all resources automatically (LIFO stack)
- Verifies TrueNAS backend cleanup

**View test results:** [Test Dashboard](https://fenio.github.io/tns-csi/dashboard/)

## Test Results and History

### CI/CD Badges

- [![CI](https://github.com/fenio/tns-csi/actions/workflows/ci.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/ci.yml) - Unit tests and sanity tests
- [![Integration Tests](https://github.com/fenio/tns-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/integration.yml) - Full Ginkgo E2E test suite

### Test Dashboard

Interactive test results dashboard with history and metrics:
- https://fenio.github.io/tns-csi/dashboard/
- Shows pass/fail status for all tests
- Tracks test duration over time
- Identifies flaky tests and patterns

## Running Tests Locally

### Prerequisites

- Go 1.21+
- [Ginkgo CLI](https://onsi.github.io/ginkgo/#getting-started): `go install github.com/onsi/ginkgo/v2/ginkgo@latest`
- Access to a TrueNAS Scale 25.10+ server
- Kubernetes cluster (k3s recommended)
- TrueNAS API key with admin privileges
- For NFS: `nfs-common` installed
- For NVMe-oF: `nvme-cli` installed, kernel modules loaded

### Environment Variables

```bash
export TRUENAS_HOST="your-truenas-ip"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"
export KUBECONFIG="$HOME/.kube/config"
```

### CSI Sanity Tests

```bash
cd tests/sanity
./test-sanity.sh
```

### Ginkgo E2E Tests

```bash
# Run all E2E tests
ginkgo -v --timeout=60m ./tests/e2e/...

# Run NFS tests only
ginkgo -v --timeout=25m ./tests/e2e/nfs/...

# Run NVMe-oF tests only
ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...

# Run shared tests only
ginkgo -v --timeout=25m ./tests/e2e/

# Run specific test by name
ginkgo -v --focus="expand" ./tests/e2e/nfs/...

# Run with verbose output
ginkgo -v -vv ./tests/e2e/nfs/...

# Generate JUnit report
ginkgo -v --junit-report=junit-nfs.xml ./tests/e2e/nfs/...
```

**Note:** E2E tests assume a Kubernetes cluster with kubectl access. They will deploy the CSI driver, run tests, and clean up.

### Using Makefile Targets

```bash
# Run all E2E tests
make test-e2e

# Run NFS E2E tests
make test-e2e-nfs

# Run NVMe-oF E2E tests
make test-e2e-nvmeof
```

## Test Coverage

### What's Tested

- **CSI Spec Compliance** - Full CSI spec validation via csi-test
- **Volume Lifecycle** - Create, attach, mount, unmount, detach, delete
- **Volume Expansion** - Dynamic resizing (NFS & NVMe-oF)
- **Snapshots** - Create, restore, clone (NFS & NVMe-oF)
- **StatefulSets** - VolumeClaimTemplates and pod identity
- **Data Persistence** - Data survives pod restarts
- **Concurrent Operations** - Race condition detection
- **Connection Resilience** - WebSocket reconnection
- **Resource Cleanup** - Orphaned resource detection
- **ZFS Properties** - Custom compression, recordsize, etc.
- **Block Mode** - Raw block device support (NVMe-oF)

### What's Not Yet Tested

- **Multi-node scenarios** - Tests run on single-node k3s
- **Network partitions** - Not tested yet
- **Storage pool failures** - Not tested yet
- **Long-running workloads** - No soak tests yet
- **Performance benchmarks** - No formal performance testing

## Contributing Tests

When adding new features:

1. Add unit tests in `pkg/*/`
2. Add Ginkgo E2E test in appropriate `tests/e2e/` subdirectory:
   - `tests/e2e/nfs/` for NFS-specific tests
   - `tests/e2e/nvmeof/` for NVMe-oF-specific tests
   - `tests/e2e/` for shared/cross-protocol tests
3. Follow the existing test patterns (see `tests/e2e/README.md` for templates)
4. Ensure tests run on real infrastructure (no mocks for E2E tests)
5. Update this documentation if adding new test categories

See [CONTRIBUTING.md](../CONTRIBUTING.md) for details.

## Troubleshooting Test Failures

### Common Issues

**Test fails with "connection refused":**
- Verify TRUENAS_HOST is correct and reachable
- Check TrueNAS API is running (should respond on /api/current)

**Test fails with "unauthorized":**
- Verify TRUENAS_API_KEY is valid
- Check API key has admin privileges

**NFS test fails with "mount failed":**
- Ensure nfs-common is installed on test node
- Check TrueNAS NFS service is enabled

**NVMe-oF test fails with "nvme connect failed":**
- Ensure nvme-cli is installed on test node
- Verify kernel modules: `nvme-tcp`, `nvme-fabrics`
- Check TrueNAS NVMe-oF service is enabled
- Verify port 4420 is accessible

**Test cleanup fails:**
- May need to manually delete datasets/shares in TrueNAS UI

**Ginkgo-specific issues:**
- Use `-v -vv` for verbose output
- Check `GinkgoWriter` output in test logs
- Use `--focus` to isolate failing tests

### Getting Help

- Check test logs in GitHub Actions runs
- Review [Test Dashboard](https://fenio.github.io/tns-csi/dashboard/) for patterns
- Open an issue with test failure details

## References

- [Ginkgo Documentation](https://onsi.github.io/ginkgo/) - BDD testing framework
- [Gomega Documentation](https://onsi.github.io/gomega/) - Matcher/assertion library
- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) - CSI specification sanity tests
- [CSI Specification](https://github.com/container-storage-interface/spec) - Official CSI spec
- [GitHub Actions Workflows](../.github/workflows/) - CI/CD configuration
