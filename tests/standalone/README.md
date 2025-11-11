# Standalone Test Suite

**Keep It Simple, Stupid**

This is a completely self-contained test suite with ZERO dependencies on existing test infrastructure. Each test is a standalone bash script that does everything from scratch.

## Philosophy

- **No dependencies**: No common libraries, no shared functions, no helper scripts
- **Self-contained**: Each test builds, deploys, tests, and cleans up
- **Simple**: Easy to read, easy to understand, easy to debug
- **One test, one purpose**: Each test validates one specific functionality

## Test 1: NFS Volume Creation

**File**: `01-test-nfs-create.sh`

**What it does**:
1. Verifies cluster access
2. Creates test namespace
3. Builds CSI driver from source (`make build`)
4. Imports image to k3s
5. Creates TrueNAS credentials secret
6. Deploys CSI driver with Helm
7. Waits for driver pods to be ready
8. Creates a 1Gi NFS PVC
9. Waits for PVC to bind
10. Creates a test pod that mounts the volume
11. Waits for pod to be ready
12. Writes a test file to the volume
13. Reads the file back and verifies content
14. Cleans up everything

**Run it**:
```bash
export TRUENAS_HOST="your-truenas-ip"
export TRUENAS_API_KEY="your-api-key"
export TRUENAS_POOL="your-pool-name"

./tests/standalone/01-test-nfs-create.sh
```

## GitHub Actions Workflow

**File**: `.github/workflows/standalone.yml`

Minimal workflow that:
- Uses `fenio/setup-k3s@main` for fresh k3s cluster
- Sets up Go for building the driver
- Runs the standalone test
- Uses self-hosted runner with TrueNAS access

**Trigger it**:
```bash
gh workflow run standalone.yml
```

## What's Different From Other Tests?

**Old integration tests**:
- Depend on `lib/common.sh` helper library
- Depend on `.github/actions/setup-dependencies`
- Reuse manifests from `manifests/` directory
- Share configuration across tests

**These standalone tests**:
- ✓ Zero external dependencies
- ✓ Build everything in the test itself
- ✓ Create all manifests inline (heredoc)
- ✓ Self-contained cleanup
- ✓ Can copy-paste and run anywhere

## Adding More Tests

Just copy the pattern:

```bash
#!/bin/bash
set -e

TEST_NAME="Your Test"
NAMESPACE="test-something-$$"

cleanup() {
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found=true || true
}
trap cleanup EXIT

# Step 1: Do something
echo "Step 1: ..."
# ... implementation

# Step 2: Do something else
echo "Step 2: ..."
# ... implementation

echo "✓ ${TEST_NAME}: PASSED"
```

That's it. No imports, no functions, no libraries.

## Why This Approach?

Sometimes you just want a simple script that does ONE thing from start to finish without having to understand a complex test framework. These tests are:

- Easy to understand (linear flow, no jumps to other files)
- Easy to debug (all code is right there)
- Easy to modify (change one test without affecting others)
- Easy to run (just bash + environment variables)

**Keep It Simple, Stupid.**
