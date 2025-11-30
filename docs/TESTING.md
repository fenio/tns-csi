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

## Automated Test Suite

### CSI Specification Compliance

**Sanity Tests:**
- Uses [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) v5.2.0
- Validates full CSI specification compliance
- Tests all CSI RPC calls and error conditions
- Location: `tests/sanity/`
- Run on: Every CI build

### Integration Tests

Every push to main triggers comprehensive integration tests:

#### Core Functionality Tests

**Basic Volume Operations (NFS & NVMe-oF):**
- `test-nfs.sh` - NFS volume provisioning and deletion
- `test-nvmeof.sh` - NVMe-oF volume provisioning and deletion
- Tests: Create PVC → Bind PV → Mount to pod → Write data → Verify → Cleanup

**Volume Expansion:**
- `test-volume-expansion-nfs.sh` - NFS volume resizing
- `test-volume-expansion-nvmeof.sh` - NVMe-oF volume resizing
- Tests dynamic volume resizing
- Verifies both storage backend and filesystem expansion

**Snapshot Operations (NFS & NVMe-oF):**
- `test-snapshot-nfs.sh` - NFS snapshot creation and restoration
- `test-snapshot-nvmeof.sh` - NVMe-oF snapshot creation and restoration
- `test-snapshot-restore.sh` - Volume restoration from snapshots
- Tests: Create volume → Write data → Snapshot → Restore from snapshot → Verify data

**StatefulSet Support:**
- `test-statefulset-nfs.sh` - NFS StatefulSet with 3 replicas
- `test-statefulset-nvmeof.sh` - NVMe-oF StatefulSet with 3 replicas
- Tests: VolumeClaimTemplates → Pod identity persistence → Volume management

**Data Persistence:**
- `test-persistence-nfs.sh` - NFS data survives pod restarts
- `test-persistence-nvmeof.sh` - NVMe-oF data survives pod restarts
- `test-pod-restart.sh` - Pod restart behavior
- Tests: Write data → Delete pod → Recreate pod → Verify data intact

**Access Modes:**
- `test-access-modes.sh` - RWO/RWX access mode testing
- `test-dual-mount.sh` - Dual mount scenarios

#### Stress & Reliability Tests

**Concurrent Operations:**
- `test-concurrent-nfs.sh` - 5 simultaneous NFS volume creations
- `test-concurrent-nvmeof.sh` - 5 simultaneous NVMe-oF volume creations
- `test-volume-stress.sh` - Volume stress testing
- Tests: Race condition detection, concurrent API calls, resource locking

**Connection Resilience:**
- `test-connection-resilience.sh` - WebSocket reconnection testing
- Tests: Controller restart during operations, automatic reconnection

**Resource Cleanup:**
- `test-orphaned-resources.sh` - Orphaned resource detection
- Tests: Cleanup of abandoned resources, TrueNAS state consistency

### Test Execution

Tests run sequentially on a single self-hosted runner with:
- Fresh k3s cluster for each test (destroyed and recreated)
- Real CSI driver deployment via Helm charts
- Actual TrueNAS API connections and storage operations
- Full cleanup after each test (PVs, datasets, NFS shares, NVMe-oF namespaces)

**Total test suite runtime:** ~10-15 minutes (optimized with caching)

**View test results:** [Test Dashboard](https://fenio.github.io/tns-csi/dashboard/)

## Test Results and History

### CI/CD Badges

- [![CI](https://github.com/fenio/tns-csi/actions/workflows/ci.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/ci.yml) - Unit tests and sanity tests
- [![Integration Tests](https://github.com/fenio/tns-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/integration.yml) - Full integration test suite

### Test Dashboard

Interactive test results dashboard with history and metrics:
- https://fenio.github.io/tns-csi/dashboard/
- Shows pass/fail status for all tests
- Tracks test duration over time
- Identifies flaky tests and patterns

## Running Tests Locally

### Prerequisites

- Access to a TrueNAS Scale 25.10+ server
- Kubernetes cluster (k3s, Kind, or full cluster)
- TrueNAS API key with admin privileges
- For NFS: `nfs-common` installed
- For NVMe-oF: `nvme-cli` installed, kernel modules loaded

### CSI Sanity Tests

```bash
cd tests/sanity
export TRUENAS_HOST="your-truenas-ip"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"
./test-sanity.sh
```

### Integration Tests

```bash
cd tests/integration

# Set environment variables
export TRUENAS_HOST="your-truenas-ip"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"

# Run individual tests
./test-nfs.sh
./test-nvmeof.sh
./test-snapshot-nfs.sh
./test-concurrent-nfs.sh
./test-statefulset-nfs.sh
```

**Note:** Integration tests assume a clean k3s cluster. They will deploy the CSI driver, run tests, and clean up.

## Test Coverage

### What's Tested

✅ **CSI Spec Compliance** - Full CSI spec validation via csi-test  
✅ **Volume Lifecycle** - Create, attach, mount, unmount, detach, delete  
✅ **Volume Expansion** - Dynamic resizing (NFS & NVMe-oF)  
✅ **Snapshots** - Create, restore, clone (NFS & NVMe-oF)  
✅ **StatefulSets** - VolumeClaimTemplates and pod identity  
✅ **Data Persistence** - Data survives pod restarts  
✅ **Concurrent Operations** - Race condition detection  
✅ **Connection Resilience** - WebSocket reconnection  
✅ **Resource Cleanup** - Orphaned resource detection  

### What's Not Yet Tested

⚠️ **Multi-node scenarios** - Tests run on single-node k3s  
⚠️ **Network partitions** - Not tested yet  
⚠️ **Storage pool failures** - Not tested yet  
⚠️ **Long-running workloads** - No soak tests yet  
⚠️ **Performance benchmarks** - No formal performance testing  

## Contributing Tests

When adding new features:

1. Add unit tests in `pkg/*/`
2. Add integration test in `tests/integration/`
3. Update this documentation
4. Ensure tests run on real infrastructure (no mocks for integration tests)

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
- Check `cleanup-truenas-artifacts.sh` for orphaned resources
- May need to manually delete datasets/shares in TrueNAS UI

### Getting Help

- Check test logs in GitHub Actions runs
- Review [Test Dashboard](https://fenio.github.io/tns-csi/dashboard/) for patterns
- Open an issue with test failure details

## References

- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) - CSI specification sanity tests
- [CSI Specification](https://github.com/container-storage-interface/spec) - Official CSI spec
- [GitHub Actions Workflows](../.github/workflows/) - CI/CD configuration
