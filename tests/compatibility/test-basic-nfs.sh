#!/bin/bash
# NFS Basic Compatibility Test
# Tests basic NFS operations: mount, write, read, expand across different k8s distributions

set -e -E

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Reuse integration test library for common functions
source "${SCRIPT_DIR}/../integration/lib/common.sh"

PROTOCOL="NFS Basic Compatibility"
PVC_NAME="compat-test-pvc-nfs"
POD_NAME="compat-test-pod-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Detect Kubernetes distribution
detect_k8s_distribution() {
    if kubectl get nodes -o json | grep -q "k3s"; then
        echo "k3s"
    elif kubectl get nodes -o json | grep -q "k0s"; then
        echo "k0s"
    elif kubectl get nodes -o json | grep -q "minikube"; then
        echo "minikube"
    elif kubectl get nodes -o json | grep -q "kubesolo"; then
        echo "kubesolo"
    else
        echo "unknown"
    fi
}

K8S_DISTRO=$(detect_k8s_distribution)

echo "=========================================================="
echo "TrueNAS CSI - NFS Basic Compatibility Test"
echo "Kubernetes Distribution: ${K8S_DISTRO}"
echo "=========================================================="
echo ""
echo "This test verifies:"
echo "  • PVC creation and binding"
echo "  • Pod mounting NFS volume"
echo "  • Write/read data operations"
echo "  • Volume expansion"
echo "=========================================================="

# Trap errors and cleanup
trap 'echo "=== ERR TRAP EXECUTING ===" >&2; \
      echo "TRAP: Step 1 - show_diagnostic_logs" >&2; \
      show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; \
      echo "TRAP: Step 2 - cleanup_test" >&2; \
      cleanup_test "${POD_NAME}" "${PVC_NAME}"; \
      echo "TRAP: Step 3 - test_summary FAILED" >&2; \
      test_summary "${PROTOCOL} (${K8S_DISTRO})" "FAILED"; \
      echo "TRAP: Step 4 - exit 1 (THIS SHOULD NEVER PRINT)" >&2; \
      exit 1' ERR

# Run basic test steps
verify_cluster
deploy_driver "nfs"
echo "DEBUG: About to call wait_for_driver" >&2
wait_for_driver || {
    echo "ERROR: wait_for_driver failed explicitly (returned $?)" >&2
    echo "ERROR: Triggering explicit failure path" >&2
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    cleanup_test "${POD_NAME}" "${PVC_NAME}"
    test_summary "${PROTOCOL} (${K8S_DISTRO})" "FAILED"
}
echo "DEBUG: wait_for_driver succeeded, continuing with tests" >&2

#######################################
# Test 1: Create PVC
#######################################
test_step 1 5 "Creating NFS PVC"

test_info "Creating PVC: ${PVC_NAME}"
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

PV_NAME=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
test_success "PVC bound to PV: ${PV_NAME}"

#######################################
# Test 2: Create Pod and mount volume
#######################################
test_step 2 5 "Creating pod and mounting NFS volume"

test_info "Creating pod: ${POD_NAME}"
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Pod ready with NFS volume mounted"

#######################################
# Test 3: Write and read data
#######################################
test_step 3 5 "Testing write and read operations"

test_info "Writing test data..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c \
    "echo 'Hello from ${K8S_DISTRO}' > /data/test.txt"

test_info "Reading test data..."
READ_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)

if [[ "${READ_DATA}" == "Hello from ${K8S_DISTRO}" ]]; then
    test_success "Data written and read successfully"
else
    test_error "Data mismatch: expected 'Hello from ${K8S_DISTRO}', got '${READ_DATA}'"
    exit 1
fi

# Write a larger file to test I/O
test_info "Writing larger file (10MB)..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c \
    "dd if=/dev/zero of=/data/largefile bs=1M count=10"

test_info "Verifying file size..."
FILE_SIZE=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- stat -c %s /data/largefile)
EXPECTED_SIZE=$((10 * 1024 * 1024))

if [[ "${FILE_SIZE}" -eq "${EXPECTED_SIZE}" ]]; then
    test_success "Large file I/O test passed (${FILE_SIZE} bytes)"
else
    test_error "File size mismatch: expected ${EXPECTED_SIZE}, got ${FILE_SIZE}"
    exit 1
fi

#######################################
# Test 4: Volume expansion
#######################################
test_step 4 5 "Testing volume expansion"

test_info "Expanding volume from 1Gi to 2Gi..."

# Patch PVC to request larger size
kubectl patch pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" \
    -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}'

# Wait for expansion to complete
test_info "Waiting for volume expansion..."
retries=0
while [[ $retries -lt 60 ]]; do
    PVC_SIZE=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.capacity.storage}')
    if [[ "${PVC_SIZE}" == "2Gi" ]]; then
        test_success "Volume expanded to 2Gi"
        break
    fi
    sleep 2
    retries=$((retries + 1))
done

if [[ $retries -ge 60 ]]; then
    test_error "Volume expansion timeout"
    exit 1
fi

# Verify filesystem sees the expanded size
test_info "Verifying filesystem expansion..."
DF_OUTPUT=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -h /data)
test_success "Filesystem expansion verified"
echo "${DF_OUTPUT}"

#######################################
# Test 5: Cleanup
#######################################
test_step 5 5 "Cleaning up resources"

cleanup_test "${POD_NAME}" "${PVC_NAME}"
test_success "Cleanup complete"

echo ""
echo "=========================================================="
echo "NFS Basic Compatibility Test Summary (${K8S_DISTRO})"
echo "=========================================================="
echo ""
echo "✓ PVC creation and binding"
echo "✓ Pod mounting NFS volume"
echo "✓ Write/read data operations"
echo "✓ Large file I/O (10MB)"
echo "✓ Volume expansion (1Gi → 2Gi)"
echo ""
echo "Result: All basic operations successful on ${K8S_DISTRO}"
echo ""
echo "=========================================================="

# Success
test_summary "${PROTOCOL} (${K8S_DISTRO})" "PASSED"
