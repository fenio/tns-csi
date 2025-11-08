# CSI Sanity Live Tests

This directory contains tests that run the official [csi-sanity](https://github.com/kubernetes-csi/csi-test/tree/master/cmd/csi-sanity) binary against a live k3s cluster with the tns-csi driver deployed.

## Overview

Unlike the mock-based sanity tests in `tests/sanity/`, these tests:

1. **Deploy a real k3s cluster** - Uses the self-hosted GitHub runner infrastructure
2. **Deploy the actual CSI driver** - Installs tns-csi-driver via Helm chart
3. **Connect to real TrueNAS** - Tests against actual TrueNAS server (not mocks)
4. **Run csi-sanity binary** - Official Kubernetes CSI compliance test suite

## What is csi-sanity?

`csi-sanity` is the official CSI specification compliance test tool maintained by the Kubernetes CSI project. It validates that a CSI driver correctly implements the CSI spec by:

- Testing Identity, Controller, and Node service RPCs
- Verifying proper error handling and edge cases
- Validating volume lifecycle operations (create, stage, publish, unpublish, unstage, delete)
- Testing volume capabilities and access modes
- Checking idempotency of operations

## Test Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  GitHub Actions (Self-Hosted Runner)                        │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  k3s Cluster                                           │ │
│  │                                                        │ │
│  │  ┌──────────────────┐      ┌──────────────────┐      │ │
│  │  │ tns-csi-controller│     │  tns-csi-node    │      │ │
│  │  │  (Controller Pod) │     │   (DaemonSet)    │      │ │
│  │  └──────────────────┘      └──────────────────┘      │ │
│  │           │                          │                │ │
│  │           │        CSI Socket        │                │ │
│  │           │  /var/lib/kubelet/       │                │ │
│  │           │  plugins/tns.csi.        │                │ │
│  │           │  truenas.com/csi.sock    │                │ │
│  │           └──────────┬───────────────┘                │ │
│  │                      │                                │ │
│  │           ┌──────────▼──────────┐                     │ │
│  │           │   csi-sanity        │                     │ │
│  │           │   (Test Runner)     │                     │ │
│  │           └─────────────────────┘                     │ │
│  └────────────────────────────────────────────────────────┘ │
│                           │                                  │
│                           │ TrueNAS API (WebSocket)          │
│                           ▼                                  │
│                  ┌─────────────────┐                         │
│                  │  TrueNAS Server │                         │
│                  │  (Self-Hosted)  │                         │
│                  └─────────────────┘                         │
└─────────────────────────────────────────────────────────────┘
```

## Test Execution

The workflow runs two separate test jobs:

### 1. NFS Protocol Test (`sanity-live-nfs`)
- Deploys driver with `truenas.protocol=nfs`
- Runs csi-sanity against NFS volumes
- Tests NFS-specific volume operations

### 2. NVMe-oF Protocol Test (`sanity-live-nvmeof`)
- Deploys driver with `truenas.protocol=nvmeof`
- Runs csi-sanity against NVMe-oF volumes
- Tests block storage operations

## Running Tests

### Via GitHub Actions (Recommended)

Tests run automatically on:
- Push to `main` branch
- Pull requests to `main`
- Push to `feature/csi-sanity-live` branch
- Manual workflow dispatch

Monitor runs at: https://github.com/fenio/tns-csi/actions

### Manual Execution (On Self-Hosted Runner)

If you need to run tests manually:

```bash
# Ensure k3s cluster is set up
./scripts/setup-kind-nfs.sh  # or use k3s setup action

# Install csi-sanity
go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest

# Deploy the driver (NFS example)
kubectl create namespace tns-csi
kubectl create secret generic truenas-csi-secret \
  --from-literal=apikey="${TRUENAS_API_KEY}" \
  --namespace=tns-csi

helm upgrade --install tns-csi-driver ./charts/tns-csi-driver \
  --namespace tns-csi \
  --set truenas.host="${TRUENAS_HOST}" \
  --set truenas.pool="${TRUENAS_POOL}" \
  --set truenas.protocol="nfs" \
  --set truenas.allowInsecure=true \
  --wait --timeout=5m

# Run the test
./tests/sanity-live/test-csi-sanity-live.sh nfs
```

## Test Configuration

The test script (`test-csi-sanity-live.sh`) configures csi-sanity with:

- **CSI Socket**: `/var/lib/kubelet/plugins/tns.csi.truenas.com/csi.sock`
- **Staging Directory**: `/tmp/csi-sanity-staging-{protocol}/`
- **Target Directory**: `/tmp/csi-sanity-target-{protocol}/`
- **Output**: Verbose Ginkgo output with progress reporting

## Expected Results

csi-sanity runs approximately 70-80 tests covering:

- ✅ Identity service (GetPluginInfo, GetPluginCapabilities, Probe)
- ✅ Controller service (CreateVolume, DeleteVolume, ControllerPublishVolume, etc.)
- ✅ Node service (NodeStageVolume, NodePublishVolume, NodeUnpublishVolume, etc.)
- ✅ Snapshot operations (if supported)
- ✅ Volume expansion (if supported)

All tests should pass for the driver to be considered CSI-compliant.

## Differences from Mock Sanity Tests

| Aspect | Mock Tests (`tests/sanity/`) | Live Tests (`tests/sanity-live/`) |
|--------|------------------------------|-----------------------------------|
| **Execution** | In-process Go tests | External csi-sanity binary |
| **Backend** | Mock TrueNAS client | Real TrueNAS server |
| **Cluster** | No Kubernetes required | Real k3s cluster |
| **Driver** | Embedded in test | Deployed via Helm |
| **Purpose** | Unit testing, fast feedback | Integration testing, CSI compliance |
| **Speed** | Fast (~10 seconds) | Slower (~5-10 minutes) |
| **Dependencies** | None (fully isolated) | Requires TrueNAS server |

## Troubleshooting

### Socket Not Found

If csi-sanity can't find the socket:

```bash
# Check if driver pods are running
kubectl get pods -n tns-csi

# Verify socket exists
ls -la /var/lib/kubelet/plugins/tns.csi.truenas.com/

# Check node pod logs
kubectl logs -n tns-csi -l app=tns-csi-node
```

### Tests Fail

1. **Check driver logs**:
   ```bash
   kubectl logs -n tns-csi -l app=tns-csi-controller
   kubectl logs -n tns-csi -l app=tns-csi-node
   ```

2. **Verify TrueNAS connectivity**:
   - Check if secrets are correctly configured
   - Verify network connectivity to TrueNAS server
   - Check TrueNAS API key permissions

3. **Review csi-sanity output**:
   - Look for specific test failures
   - Check error messages for CSI spec violations

## CI/CD Integration

This test is integrated into the CI/CD pipeline as a separate workflow (`csi-sanity-live.yml`). It complements the existing integration tests by providing:

- Official CSI spec compliance validation
- Independent verification from Kubernetes community tool
- Additional edge case coverage from csi-sanity test suite

## Contributing

When modifying these tests:

1. **Test both protocols** - Always verify NFS and NVMe-oF
2. **Update this README** - Document any configuration changes
3. **Check GitHub Actions** - Ensure workflow runs successfully
4. **Review logs** - Check both controller and node logs on failures

## References

- [CSI Specification](https://github.com/container-storage-interface/spec)
- [csi-sanity Documentation](https://github.com/kubernetes-csi/csi-test/tree/master/cmd/csi-sanity)
- [Kubernetes CSI Developer Guide](https://kubernetes-csi.github.io/docs/)
