# Compatibility Tests

This directory contains basic compatibility tests that verify the TrueNAS CSI driver works correctly across different Kubernetes distributions.

## Overview

The compatibility test suite tests **basic CSI operations only**:
- Volume provisioning (PVC creation and binding)
- Volume mounting/attachment to pods
- Write and read data operations
- Volume expansion (resize)

These tests are run across **four Kubernetes distributions**:
- **k3s** - Lightweight Kubernetes distribution
- **k0s** - Zero-friction Kubernetes distribution
- **minikube** - Local Kubernetes development environment
- **kubesolo** - Minimalist single-node Kubernetes

## Test Structure

```
tests/compatibility/
├── README.md                  # This file
├── test-basic-nfs.sh         # NFS basic operations test
└── test-basic-nvmeof.sh      # NVMe-oF basic operations test
```

## Running Tests

### Via GitHub Actions (Recommended)

Tests run automatically on:
- Every push to `main`
- Every pull request
- Weekly on Mondays at 3 AM UTC
- Manual trigger via workflow dispatch

View test results: https://github.com/fenio/tns-csi/actions/workflows/compatibility.yml

### Locally

To run compatibility tests locally:

```bash
# Set required environment variables
export TRUENAS_HOST="your-truenas-host"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool"

# Setup your preferred k8s distribution
# Example for k3s:
curl -sfL https://get.k3s.io | sh -

# Build and load the driver image
make docker-build
sudo k3s ctr images import tns-csi-latest.tar

# Run NFS test
./tests/compatibility/test-basic-nfs.sh

# Run NVMe-oF test
./tests/compatibility/test-basic-nvmeof.sh
```

## Test Workflow

Each compatibility test follows this pattern:

1. **Detect K8s Distribution** - Identifies which distribution is running
2. **Deploy CSI Driver** - Installs the driver with Helm
3. **Create PVC** - Tests volume provisioning
4. **Create Pod** - Tests volume mounting/attachment
5. **Test I/O** - Writes and reads data (small and 10MB files)
6. **Test Expansion** - Expands volume from 1Gi to 2Gi
7. **Cleanup** - Removes all test resources

## Test Matrix

| Distribution | NFS | NVMe-oF | Notes |
|-------------|-----|---------|-------|
| k3s         | ✅  | ✅      | Primary development environment |
| k0s         | ✅  | ✅      | Zero-friction distribution |
| minikube    | ✅  | ✅      | Local development environment |
| kubesolo    | ✅  | ✅      | Minimalist single-node |

## Key Differences from Integration Tests

**Compatibility Tests:**
- Test basic operations only (mount, I/O, resize)
- Run across multiple Kubernetes distributions
- Focus on distribution compatibility
- Lighter weight, faster execution

**Integration Tests** (temporarily disabled):
- Test advanced features (snapshots, persistence, etc.)
- Run on k3s only
- Test comprehensive CSI functionality
- More thorough but slower

## GitHub Actions Setup

The workflow uses custom setup actions:

- `.github/actions/setup-k3s` - Sets up fresh k3s cluster
- `.github/actions/setup-k0s` - Sets up fresh k0s cluster
- `.github/actions/setup-minikube` - Sets up fresh minikube cluster
- `fenio/setup-kubesolo@v1` - External action for kubesolo setup

Each test job:
1. Checks out the repository
2. Sets up dependencies (builds Docker image)
3. Sets up the specific Kubernetes distribution
4. Runs the compatibility test
5. Reports results

The cleanup job runs at the end to remove any leftover TrueNAS resources.

## NVMe-oF Configuration

NVMe-oF tests require proper TrueNAS configuration:
- NVMe-oF TCP portal configured in TrueNAS UI
- At least one NVMe-oF port available

If NVMe-oF is not configured, tests will be **SKIPPED** (not failed).

## Troubleshooting

### Test Failures

Check the GitHub Actions logs for details:
1. Go to Actions tab
2. Select the failed workflow run
3. Check the specific job that failed
4. Review logs for error messages

### Local Test Issues

**PVC not binding:**
- Check driver pods are running: `kubectl get pods -n kube-system`
- Check controller logs: `kubectl logs -n kube-system <controller-pod> -c tns-csi-plugin`

**NVMe-oF test skipped:**
- Verify NVMe-oF is configured in TrueNAS UI
- Check for NVMe-oF TCP portals

**Pod not starting:**
- Check events: `kubectl describe pod <pod-name> -n <namespace>`
- Check node logs: `kubectl logs -n kube-system <node-pod> -c tns-csi-plugin`

## Contributing

When adding new compatibility tests:
1. Keep tests focused on basic operations
2. Test both NFS and NVMe-oF protocols
3. Include proper error handling and cleanup
4. Add detection for new Kubernetes distributions if needed
5. Update this README with any new requirements

## Related Documentation

- [Main README](../../README.md)
- [Integration Tests](../integration/README.md)
- [Deployment Guide](../../docs/DEPLOYMENT.md)
- [Testing Guide](../../docs/TESTING.md)
