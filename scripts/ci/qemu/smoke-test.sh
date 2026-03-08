#!/usr/bin/env bash
#
# Smoke test: Create and mount a PVC for each storage protocol.
#
# For each protocol (NFS, iSCSI, NVMe-oF, and optionally SMB):
#   1. Create a PVC
#   2. Create a Pod that mounts it and writes a test file
#   3. Wait for the Pod to be Running
#   4. Verify the file was written
#   5. Clean up
#
# This validates the full CSI path: CreateVolume -> NodeStage -> NodePublish -> mount.

set -euo pipefail

NAMESPACE="smoke-test"
TIMEOUT="30s"
PASSED=0
FAILED=0
ERRORS=""

# ── Helpers ─────────────────────────────────────────────────────────

create_namespace() {
    kubectl create namespace "$NAMESPACE" 2>/dev/null || true
}

cleanup_namespace() {
    echo ""
    echo "=== Cleaning up namespace $NAMESPACE ==="
    kubectl delete namespace "$NAMESPACE" --timeout=120s 2>/dev/null || true
    for i in $(seq 1 30); do
        if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
            echo "Namespace deleted"
            return
        fi
        sleep 2
    done
    echo "WARNING: Namespace deletion timed out"
}

wait_for_pod_running() {
    local pod_name="$1"
    echo "  Waiting for pod $pod_name to be Running (timeout: $TIMEOUT)..."
    if kubectl wait --for=condition=Ready "pod/$pod_name" \
        -n "$NAMESPACE" --timeout="$TIMEOUT" 2>&1; then
        echo "  Pod $pod_name is Ready"
        return 0
    else
        echo "  ERROR: Pod $pod_name did not become Ready"
        echo "  --- Pod describe ---"
        kubectl describe pod "$pod_name" -n "$NAMESPACE" 2>&1 | tail -30 || true
        echo "  --- Events ---"
        kubectl get events -n "$NAMESPACE" --field-selector "involvedObject.name=$pod_name" 2>&1 || true
        return 1
    fi
}

verify_file() {
    local pod_name="$1"
    local expected_content="$2"
    echo "  Verifying test file..."
    local actual
    actual=$(kubectl exec "$pod_name" -n "$NAMESPACE" -- cat /data/test.txt 2>&1) || {
        echo "  ERROR: Could not read test file"
        return 1
    }
    if [ "$actual" = "$expected_content" ]; then
        echo "  File content verified: '$actual'"
        return 0
    else
        echo "  ERROR: File content mismatch. Expected '$expected_content', got '$actual'"
        return 1
    fi
}

cleanup_resources() {
    local name="$1"
    echo "  Cleaning up $name..."
    kubectl delete pod "$name" -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
    kubectl wait --for=delete "pod/$name" -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
    kubectl delete pvc "$name" -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
    kubectl wait --for=delete "pvc/$name" -n "$NAMESPACE" --timeout=120s 2>/dev/null || true
}

# ── Test Functions ──────────────────────────────────────────────────

test_protocol() {
    local protocol="$1"
    local sc_name="$2"
    local name="smoke-${protocol}"

    echo ""
    echo "========================================"
    echo "  Testing: $protocol ($sc_name)"
    echo "========================================"

    # Verify storage class exists
    if ! kubectl get sc "$sc_name" >/dev/null 2>&1; then
        echo "  SKIPPED: StorageClass $sc_name not found"
        return
    fi

    # Create PVC
    echo "  Creating PVC..."
    kubectl apply -n "$NAMESPACE" -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $name
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $sc_name
  resources:
    requests:
      storage: 1Gi
EOF

    # Create Pod that mounts PVC and writes a file
    echo "  Creating Pod..."
    kubectl apply -n "$NAMESPACE" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $name
spec:
  containers:
  - name: test
    image: busybox:stable
    command:
    - sh
    - -c
    - |
      echo "hello-from-${protocol}" > /data/test.txt
      sleep 3600
    volumeMounts:
    - name: vol
      mountPath: /data
  volumes:
  - name: vol
    persistentVolumeClaim:
      claimName: $name
  tolerations:
  - operator: Exists
  terminationGracePeriodSeconds: 5
EOF

    # Wait and verify
    if wait_for_pod_running "$name"; then
        if verify_file "$name" "hello-from-${protocol}"; then
            echo "  PASSED: $protocol"
            PASSED=$((PASSED + 1))
        else
            echo "  FAILED: $protocol (file verification)"
            FAILED=$((FAILED + 1))
            ERRORS="${ERRORS}\n  - $protocol: file verification failed"
        fi
    else
        echo "  FAILED: $protocol (pod not ready)"
        FAILED=$((FAILED + 1))
        ERRORS="${ERRORS}\n  - $protocol: pod did not become ready"
    fi

    cleanup_resources "$name"
}

# ── Main ────────────────────────────────────────────────────────────

echo "========================================"
echo "  QEMU E2E Smoke Tests"
echo "========================================"

echo ""
echo "=== StorageClasses ==="
kubectl get sc | grep tns-csi || {
    echo "ERROR: No tns-csi StorageClasses found"
    exit 1
}

create_namespace

test_protocol "nfs"    "tns-csi-nfs"
test_protocol "iscsi"  "tns-csi-iscsi"
test_protocol "nvmeof" "tns-csi-nvmeof"

# SMB is optional (requires secrets)
if [ -n "${SMB_USERNAME:-}" ]; then
    test_protocol "smb" "tns-csi-smb"
else
    echo ""
    echo "  SKIPPED: SMB (no SMB_USERNAME set)"
fi

cleanup_namespace

# ── Summary ─────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo "  Results: $PASSED passed, $FAILED failed"
echo "========================================"

if [ "$FAILED" -gt 0 ]; then
    echo -e "Failures:${ERRORS}"
    exit 1
fi

echo "All smoke tests passed!"
