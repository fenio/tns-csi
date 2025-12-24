# E2E Tests for TrueNAS CSI Driver

This directory contains end-to-end tests for the TrueNAS CSI driver using [Ginkgo](https://onsi.github.io/ginkgo/) and [Gomega](https://onsi.github.io/gomega/).

## Prerequisites

- Go 1.21+
- `kubectl` configured with cluster access
- `helm` CLI installed
- Access to a TrueNAS server with API key
- A Kubernetes cluster (k3s, k0s, minikube, etc.)

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `TRUENAS_HOST` | Yes | - | TrueNAS server hostname (e.g., `truenas.local`) |
| `TRUENAS_API_KEY` | Yes | - | TrueNAS API key for authentication |
| `TRUENAS_POOL` | No | `csi` | ZFS pool to use for test volumes |
| `CSI_IMAGE_REPO` | No | `ghcr.io/fenio/tns-csi` | Docker image repository |
| `CSI_IMAGE_TAG` | No | `latest` | Docker image tag |
| `KUBECONFIG` | No | `~/.kube/config` | Path to kubeconfig file |
| `TEST_TIMEOUT` | No | `5m` | Default timeout for operations |

## Running Tests

### Run All Tests

```bash
cd tests/e2e
go test -v -timeout 20m ./...
```

### Run NFS Tests Only

```bash
cd tests/e2e
go test -v -timeout 15m ./nfs/...
```

### Run NVMe-oF Tests Only

```bash
cd tests/e2e
go test -v -timeout 15m ./nvmeof/...
```

### Run Specific Test by Name

Use Ginkgo's focus feature:

```bash
# Run only tests matching "expand"
go test -v ./nfs/... -ginkgo.focus="expand"

# Run only tests matching "Basic"
go test -v ./nfs/... -ginkgo.focus="Basic"
```

### Run with Verbose Output

```bash
go test -v ./nfs/... -ginkgo.v
```

### Generate JUnit Report

```bash
go test -v ./nfs/... -ginkgo.junit-report=junit-nfs.xml
```

## Directory Structure

```
tests/e2e/
├── e2e_suite_test.go       # Root suite (optional, for running all tests)
├── framework/
│   ├── config.go           # Test configuration from env vars
│   ├── cleanup.go          # Cleanup tracking (LIFO)
│   ├── framework.go        # Main framework struct
│   ├── helm.go             # Helm CLI wrapper
│   ├── kubernetes.go       # Kubernetes client helpers
│   └── truenas.go          # TrueNAS verification
├── nfs/
│   ├── nfs_suite_test.go   # NFS suite bootstrap
│   └── basic_test.go       # Basic NFS tests
├── nvmeof/
│   ├── nvmeof_suite_test.go # NVMe-oF suite bootstrap
│   └── basic_test.go       # Basic NVMe-oF tests
└── README.md
```

## Writing New Tests

### Basic Test Template

```go
package nfs

import (
    "context"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    corev1 "k8s.io/api/core/v1"

    "github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("My Feature", func() {
    var f *framework.Framework

    BeforeEach(func() {
        var err error
        f, err = framework.NewFramework()
        Expect(err).NotTo(HaveOccurred())
        
        err = f.Setup("nfs")
        Expect(err).NotTo(HaveOccurred())
    })

    AfterEach(func() {
        if f != nil {
            f.Teardown()
        }
    })

    It("should do something useful", func() {
        ctx := context.Background()

        By("Creating a PVC")
        pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
            Name:             "test-pvc",
            StorageClassName: "tns-csi-nfs",
            Size:             "1Gi",
            AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
        })
        Expect(err).NotTo(HaveOccurred())

        By("Waiting for PVC to be bound")
        err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
        Expect(err).NotTo(HaveOccurred())

        // ... rest of test
    })
})
```

### Key Framework Methods

| Method | Description |
|--------|-------------|
| `f.Setup(protocol)` | Deploy driver, create namespace |
| `f.Teardown()` | Clean up all resources |
| `f.CreatePVC(ctx, opts)` | Create PVC with auto-cleanup |
| `f.CreatePod(ctx, opts)` | Create Pod with auto-cleanup |
| `f.K8s.WaitForPVCBound(ctx, name, timeout)` | Wait for PVC to bind |
| `f.K8s.WaitForPodReady(ctx, name, timeout)` | Wait for Pod to be ready |
| `f.K8s.ExecInPod(ctx, name, cmd)` | Execute command in pod |
| `f.K8s.ExpandPVC(ctx, name, size)` | Expand PVC capacity |
| `f.VerifyTrueNASCleanup(ctx, dataset, timeout)` | Verify backend cleanup |

### Using Eventually for Async Operations

```go
// Wait for capacity to change
Eventually(func() string {
    cap, _ := f.K8s.GetPVCCapacity(ctx, pvc.Name)
    return cap
}, 2*time.Minute, 5*time.Second).Should(Equal("3Gi"))
```

## CI Integration

Tests run automatically via GitHub Actions:

- **Workflow**: `.github/workflows/integration-e2e.yml`
- **Trigger**: Manual dispatch (workflow_dispatch)
- **Runner**: Self-hosted (`new`)

To run manually:
1. Go to Actions → "E2E Tests (Go)"
2. Click "Run workflow"
3. Select suite (all/nfs/nvmeof)
4. Optionally add a focus filter

## Comparison with Bash Tests

| Aspect | Bash Tests | Go E2E Tests |
|--------|------------|--------------|
| Location | `tests/integration/` | `tests/e2e/` |
| Framework | Custom bash library | Ginkgo/Gomega |
| Waiting | Fixed `sleep` + polling | `Eventually()` with backoff |
| Cleanup | Trap-based, error-prone | LIFO stack, guaranteed |
| Type Safety | None | Full Go type system |
| IDE Support | Limited | Full (autocomplete, refactoring) |
| Parallelism | Manual | Ginkgo-managed |

## Troubleshooting

### Tests fail to start

1. Check environment variables are set:
   ```bash
   echo $TRUENAS_HOST
   echo $TRUENAS_API_KEY
   ```

2. Verify kubectl access:
   ```bash
   kubectl get nodes
   ```

3. Verify helm is installed:
   ```bash
   helm version
   ```

### PVC stuck in Pending

Check CSI driver logs:
```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver -c csi-driver
```

### Cleanup not working

The framework uses LIFO cleanup. If tests fail mid-execution, cleanup may be incomplete.
Manual cleanup:
```bash
kubectl delete namespace -l app.kubernetes.io/managed-by=e2e-test
```
