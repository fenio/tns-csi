# Integration Test Framework

This directory contains the standardized integration test framework for the TrueNAS CSI driver. All protocols (NFS, NVMe-oF) are tested using the same approach for consistency and maintainability.

## Directory Structure

```
tests/integration/
├── lib/
│   └── common.sh           # Shared test library functions
├── manifests/
│   ├── pvc-nfs.yaml        # NFS PVC definition
│   ├── pod-nfs.yaml        # NFS test pod
│   ├── pvc-nvmeof.yaml     # NVMe-oF PVC (filesystem mode)
│   ├── pod-nvmeof.yaml     # NVMe-oF test pod (filesystem)
│   ├── pvc-nvmeof-block.yaml  # NVMe-oF PVC (block mode)
│   ├── pod-nvmeof-block.yaml  # NVMe-oF test pod (block)
│   ├── pvc-dual-nfs.yaml   # NFS PVC for dual-mount test
│   ├── pvc-dual-nvmeof.yaml # NVMe-oF PVC for dual-mount test
│   └── pod-dual-mount.yaml # Pod with both NFS and NVMe-oF volumes
├── test-nfs.sh             # NFS integration test
├── test-nvmeof.sh          # NVMe-oF integration test (filesystem)
├── test-nvmeof-block.sh    # NVMe-oF integration test (block)
├── test-dual-mount.sh      # Dual-mount test (NFS + NVMe-oF)
├── test-detached-clone.sh  # Detached clone test (zfs send/receive)
├── test-nfs-mount-options.sh # Custom NFS mount options test
└── test-btrfs-filesystem.sh  # Btrfs filesystem test (NVMe-oF)
```

## Test Workflow

Each protocol test follows the same standardized 7-step workflow:

1. **Verify Cluster** - Ensure Kubernetes cluster is accessible
2. **Deploy Driver** - Install CSI driver using Helm with protocol-specific configuration
3. **Wait for Driver** - Wait for CSI driver pods to be ready
4. **Create PVC** - Apply PVC manifest and wait for it to be bound
5. **Create Test Pod** - Apply pod manifest and wait for it to be ready
6. **Test I/O Operations** - Run read/write tests on the mounted volume or block device
7. **Cleanup** - Delete test resources (pod and PVC)

## Prerequisites

### Required Environment Variables

All test scripts require the following environment variables:

```bash
export TRUENAS_HOST="your-truenas-host.example.com"
export TRUENAS_API_KEY="your-api-key-here"
export TRUENAS_POOL="your-pool-name"
```

### Required Tools

- `kubectl` - Kubernetes CLI
- `helm` - Helm 3.x
- `make` - For building Docker images
- Protocol-specific tools:
  - NFS: `nfs-common` (Ubuntu/Debian)
  - NVMe-oF: `nvme-cli` and kernel module `nvme-tcp`

### Cluster Requirements

- A running Kubernetes cluster (k3s, kind, or full cluster)
- Cluster must have access to the TrueNAS server
- CSI driver image must be built and available

## Running Tests

### Run Individual Protocol Tests

```bash
# NFS test
./tests/integration/test-nfs.sh

# NVMe-oF test (filesystem mode)
./tests/integration/test-nvmeof.sh

# NVMe-oF test (block device mode)
./tests/integration/test-nvmeof-block.sh

# Dual-mount test (NFS + NVMe-oF simultaneously)
./tests/integration/test-dual-mount.sh

# Detached clone test (zfs send/receive independence)
./tests/integration/test-detached-clone.sh

# Custom NFS mount options test
./tests/integration/test-nfs-mount-options.sh

# Btrfs filesystem test (NVMe-oF with btrfs)
./tests/integration/test-btrfs-filesystem.sh
```

### Run All Tests

```bash
# Run all implemented protocol tests
for test in tests/integration/test-*.sh; do
    echo "Running $test..."
    "$test" || echo "Test failed: $test"
done
```

## Common Library Functions

The `lib/common.sh` library provides reusable functions for all tests:

### Test Output Functions

- `test_step(step, total, description)` - Print a test step header
- `test_success(message)` - Print success message with ✓
- `test_error(message)` - Print error message with ✗
- `test_warning(message)` - Print warning message with ⚠
- `test_info(message)` - Print info message with ℹ

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

### Configuration Variables

These can be overridden by setting environment variables before running tests:

- `TEST_NAMESPACE` (default: `default`) - Namespace for test resources
- `TIMEOUT_PVC` (default: `120s`) - Timeout for PVC binding
- `TIMEOUT_POD` (default: `120s`) - Timeout for pod readiness
- `TIMEOUT_DRIVER` (default: `120s`) - Timeout for driver readiness

## Adding New Protocol Tests

To add a test for a new protocol:

1. **Create manifests** in `manifests/`:
   ```bash
   # PVC manifest
   manifests/pvc-<protocol>.yaml
   
   # Pod manifest
   manifests/pod-<protocol>.yaml
   ```

2. **Create test script**:
   ```bash
   #!/bin/bash
   set -e
   
   SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
   source "${SCRIPT_DIR}/lib/common.sh"
   
   PROTOCOL="MyProtocol"
   PVC_NAME="test-pvc-myprotocol"
   POD_NAME="test-pod-myprotocol"
   MANIFEST_DIR="${SCRIPT_DIR}/manifests"
   
   echo "========================================"
   echo "TrueNAS CSI - MyProtocol Integration Test"
   echo "========================================"
   
   trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR
   
   verify_cluster
   deploy_driver "myprotocol"
   wait_for_driver
   create_pvc "${MANIFEST_DIR}/pvc-myprotocol.yaml" "${PVC_NAME}"
   create_test_pod "${MANIFEST_DIR}/pod-myprotocol.yaml" "${POD_NAME}"
   test_io_operations "${POD_NAME}" "/data" "filesystem"
   cleanup_test "${POD_NAME}" "${PVC_NAME}"
   
   test_summary "${PROTOCOL}" "PASSED"
   ```

3. **Make script executable**:
   ```bash
   chmod +x tests/integration/test-myprotocol.sh
   ```

4. **Update `deploy_driver()` in `lib/common.sh`** to support the new protocol

5. **Add to CI/CD** in `.github/workflows/integration.yml`

## CI/CD Integration

The GitHub Actions workflow in `.github/workflows/integration.yml` uses these test scripts:

- **Separate jobs** for each protocol (NFS, NVMe-oF)
- **Self-hosted runner** with real TrueNAS server
- **Automatic cleanup** between tests
- **Detailed logging** on failure

Each job:
1. Installs dependencies (system packages, Docker, Go, Helm, k3s)
2. Configures kubectl to access the cluster
3. Builds the CSI driver Docker image
4. Cleans up previous deployments
5. Runs the protocol-specific test script
6. Environment variables are provided via GitHub Secrets

## Troubleshooting

### Test Fails at PVC Binding

Check:
- TrueNAS API connectivity (WebSocket connection)
- Controller logs: `kubectl logs -n kube-system -l app.kubernetes.io/component=controller`
- TrueNAS configuration (pools, API key permissions)

### Test Fails at Pod Ready

Check:
- Node logs: `kubectl logs -n kube-system -l app.kubernetes.io/component=node`
- Pod events: `kubectl describe pod <pod-name>`
- Protocol-specific requirements (NFS exports, NVMe-oF targets, etc.)

### Test Fails at I/O Operations

Check:
- Volume mount: `kubectl exec <pod-name> -- df -h`
- File permissions: `kubectl exec <pod-name> -- ls -la /data`
- Node logs for mount errors

### Enable Debug Logging

Set environment variables before running tests:

```bash
export TIMEOUT_PVC="300s"    # Increase timeout
export TIMEOUT_POD="300s"    # Increase timeout
```

Or modify the test script to use higher verbosity:

```bash
# Add to beginning of test script
set -x  # Enable bash debug output
```

## Protocol-Specific Notes

### NFS

- Requires `nfs-common` package on nodes
- Uses `ReadWriteOnce` or `ReadWriteMany` access modes
- Filesystem mode only

### NVMe-oF

- Requires `nvme-cli` package and `nvme-tcp` kernel module
- Uses `ReadWriteOnce` access mode
- Supports both filesystem and block device modes
- Tests check if NVMe-oF ports are configured on TrueNAS (skips if not)

### Dual-Mount Test

- Tests simultaneous mounting of both NFS and NVMe-oF volumes in a single pod
- Verifies that both protocols can coexist and operate independently
- Requires both NFS and NVMe-oF to be properly configured on TrueNAS
- Validates volume isolation - data written to one volume doesn't appear in the other
- Tests I/O operations on both volumes concurrently
- This test is important for validating that:
  - The CSI driver can handle multiple protocols simultaneously
  - There are no conflicts between NFS and NVMe-oF volume attachment/mounting
  - Both storage backends remain fully functional when used together

### Detached Clone Test

- Tests snapshot clones created with `detachedVolumesFromSnapshots=true`
- Uses zfs send/receive instead of zfs clone for true independence
- Validates that clones survive parent volume deletion
- Key test steps:
  1. Create parent volume and write data
  2. Create snapshot with detached VolumeSnapshotClass
  3. Restore snapshot to new PVC (uses zfs send/receive)
  4. Delete parent volume
  5. Verify clone still works after parent deletion
- This proves the clone has no dependency on the parent dataset

### NFS Mount Options Test

- Tests custom NFS mount options via StorageClass parameters
- Validates that `nfsMountOptions` parameter works correctly
- Example options tested: `vers=4.1,hard,timeo=600,retrans=5,rsize=1048576,wsize=1048576`
- Verifies mount options are applied and I/O works correctly
- Useful for tuning NFS performance for specific workloads

### Btrfs Filesystem Test

- Tests NVMe-oF volumes formatted with btrfs filesystem
- Validates btrfs format, mount, and I/O operations
- Tests volume expansion with btrfs (uses `btrfs filesystem resize`)
- Requires NVMe-oF to be configured on TrueNAS
- Skips gracefully if NVMe-oF is not available

## Contributing

When modifying the test framework:

1. Ensure all protocol tests follow the same structure
2. Update this README with any new features or changes
3. Test changes with all protocol test scripts
4. Verify CI/CD integration still works
