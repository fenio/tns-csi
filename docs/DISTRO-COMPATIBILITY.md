# Kubernetes Distribution Compatibility Tests

## Overview

This test suite runs basic CSI driver functionality tests across multiple Kubernetes distributions to ensure compatibility. Unlike the full integration test suite, these tests are lightweight and focus on verifying core functionality works on different distros.

## Tested Distributions

| Distribution | Description | Setup Action |
|--------------|-------------|--------------|
| **K3s** | Lightweight Kubernetes by Rancher | [fenio/setup-k3s](https://github.com/fenio/setup-k3s) |
| **K0s** | Zero-friction Kubernetes by Mirantis | [fenio/setup-k0s](https://github.com/fenio/setup-k0s) |
| **KubeSolo** | Single-node Kubernetes | [fenio/setup-kubesolo](https://github.com/fenio/setup-kubesolo) |
| **Minikube** | Local Kubernetes for development | [fenio/setup-minikube](https://github.com/fenio/setup-minikube) |
| **Talos** | Secure, immutable Kubernetes OS | [fenio/setup-talos](https://github.com/fenio/setup-talos) |
| **MicroK8s** | Lightweight Kubernetes by Canonical | [fenio/setup-microk8s](https://github.com/fenio/setup-microk8s) |

### Compatibility Matrix

All 6 distributions are tested with NFS, NVMe-oF, and iSCSI protocols:

| Distribution | NFS | NVMe-oF | iSCSI |
|--------------|:---:|:-------:|:-----:|
| K3s | ✅ | ✅ | ✅ |
| K0s | ✅ | ✅ | ✅ |
| KubeSolo | ✅ | ✅ | ✅ |
| Minikube | ✅ | ✅ | ✅ |
| Talos | ✅ | ✅ | ✅ |
| MicroK8s | ✅ | ✅ | ✅ |

## What's Tested

Each distribution runs basic tests for NFS, NVMe-oF, and iSCSI protocols:

1. **Driver Deployment** - Helm installation and pod startup
2. **PVC Creation** - Volume provisioning
3. **Pod Creation** - Volume mounting
4. **Basic I/O** - Read/write operations
5. **Cleanup** - Resource deletion

## Running the Tests

### Via GitHub Actions (Recommended)

1. Navigate to **Actions** → **Distro Compatibility Tests**
2. Click **Run workflow**
3. Select options:
   - **Distribution**: `all` (default) or specific distro
   - **Protocol**: `both` (default), `nfs`, or `nvmeof`
4. Click **Run workflow**

### Locally (Manual)

```bash
# Set environment variables
export TRUENAS_HOST="your-truenas-host"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"
export CSI_IMAGE_TAG="latest"

# Choose protocol
export TEST_PROTOCOL="nfs"  # or "nvmeof"

# Setup Kubernetes distribution (example with K3s)
curl -sfL https://get.k3s.io | sh -

# Run the test
./tests/integration/test-distro-basic.sh
```

## Workflow Schedule

- **Manual**: Trigger anytime via GitHub Actions UI
- **Scheduled**: Runs automatically every Sunday at 3 AM UTC

## Test Duration

- **Per distro/protocol**: ~3-5 minutes
- **Full suite (all distros, both protocols)**: ~20 minutes

## Comparison with Full Integration Tests

| Feature | Distro Tests | Full Integration Tests |
|---------|--------------|------------------------|
| **Distributions** | 6 distros | K3s only |
| **Protocols** | NFS, NVMe-oF | NFS, NVMe-oF |
| **Test Coverage** | Basic functionality | Comprehensive (snapshots, expansion, etc.) |
| **Duration** | 3-5 min per distro | 60+ min total |
| **Purpose** | Verify compatibility | Validate features |
| **Frequency** | Weekly + on demand | On demand |

## Viewing Results

Test results are available in:
1. **GitHub Actions Summary** - Matrix view of distro/protocol results
2. **Job Logs** - Detailed logs for each test run
3. **Test Summary** - Consolidated pass/fail report

## Troubleshooting

### Test Failed on Specific Distro

1. Check the job logs for that specific distro
2. Look for driver deployment issues (most common)
3. Verify the distro supports NFS/NVMe-oF kernel modules

### NVMe-oF Tests Skipped

This is expected if:
- NVMe-oF ports are not configured in TrueNAS
- The test automatically detects this and skips gracefully

### All Tests Failing

Check:
1. TrueNAS connectivity from runner
2. API key validity
3. Storage pool availability

## Development

### Adding a New Distribution

1. Create a setup action (e.g., `setup-k8s-distro`)
2. Add a new job in `.github/workflows/distro-compatibility.yml`:

```yaml
distro-name-basic:
  name: "DistroName: Basic Tests"
  runs-on: new
  timeout-minutes: 20
  needs: compute-tag
  strategy:
    matrix:
      protocol: ['nfs', 'nvmeof']
  steps:
    - uses: actions/checkout@v6
    - uses: your-org/setup-distro@v1
    - name: Run Basic Test
      env:
        TRUENAS_HOST: ${{ secrets.TRUENAS_HOST }}
        TRUENAS_API_KEY: ${{ secrets.TRUENAS_API_KEY }}
        TRUENAS_POOL: ${{ secrets.TRUENAS_POOL }}
        CSI_IMAGE_TAG: ${{ needs.compute-tag.outputs.image_tag }}
        TEST_PROTOCOL: ${{ matrix.protocol }}
      run: ./tests/integration/test-distro-basic.sh
```

3. Update the summary job to include the new distro

### Extending Tests

To add more test steps to the basic test:

1. Edit `tests/integration/test-distro-basic.sh`
2. Increment `set_test_steps` count
3. Add test function calls before cleanup
4. Keep tests lightweight (avoid long-running operations)

## Related Documentation

- [Full Integration Tests](./tests/integration/README.md)
- [Testing Guide](../docs/TESTING.md)
- [CI/CD Pipeline](../docs/DEPLOYMENT.md)
