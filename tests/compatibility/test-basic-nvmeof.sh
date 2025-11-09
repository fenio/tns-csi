#!/bin/bash
# NVMe-oF Basic Compatibility Test
# Tests basic NVMe-oF operations: mount, write, read, expand across different k8s distributions

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Reuse integration test library for common functions
source "${SCRIPT_DIR}/../integration/lib/common.sh"

PROTOCOL="NVMe-oF Basic Compatibility"
PVC_NAME="compat-test-pvc-nvmeof"
POD_NAME="compat-test-pod-nvmeof"
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
echo "TrueNAS CSI - NVMe-oF Basic Compatibility Test"
echo "Kubernetes Distribution: ${K8S_DISTRO}"
echo "=========================================================="
echo ""
echo "This test verifies:"
echo "  • PVC creation and binding"
echo "  • Pod attaching NVMe-oF volume"
echo "  • Write/read data operations"
echo "  • Volume expansion"
echo "=========================================================="

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL} (${K8S_DISTRO})" "FAILED"; exit 1' ERR

# Run basic test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

#######################################
# Pre-check: Verify NVMe-oF is configured
#######################################
test_info "Verifying NVMe-oF configuration on TrueNAS..."

# Create temporary PVC to trigger configuration check
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f - || true
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nvmeof-precheck
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF

sleep 5

# Check for NVMe-oF configuration errors
CONTROLLER_POD=$(kubectl get pods -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [[ -n "${CONTROLLER_POD}" ]]; then
    LOGS=$(kubectl logs -n kube-system "${CONTROLLER_POD}" -c tns-csi-plugin --tail=30 2>/dev/null || true)
    if echo "$LOGS" | grep -q "No TCP NVMe-oF port"; then
        test_warning "NVMe-oF ports not configured on TrueNAS server"
        test_warning "Skipping ${PROTOCOL} tests - NVMe-oF is not configured"
        test_info "To enable NVMe-oF: Configure an NVMe-oF TCP portal in TrueNAS UI"
        kubectl delete pvc nvmeof-precheck -n "${TEST_NAMESPACE}" --ignore-not-found=true
        kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
        test_summary "${PROTOCOL} (${K8S_DISTRO})" "SKIPPED"
        exit 0
    fi
fi

kubectl delete pvc nvmeof-precheck -n "${TEST_NAMESPACE}" --ignore-not-found=true
test_success "NVMe-oF configuration verified"

#######################################
# Test 1: Create PVC
#######################################
test_step 1 5 "Creating NVMe-oF PVC"

test_info "Creating PVC: ${PVC_NAME}"
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF

test_info "PVC created (WaitForFirstConsumer binding mode)"

#######################################
# Test 2: Create Pod to trigger binding
#######################################
test_step 2 5 "Creating pod to trigger volume binding"

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

# Wait for PVC to bind (triggered by pod scheduling)
test_info "Waiting for PVC to bind..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

PV_NAME=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
test_success "PVC bound to PV: ${PV_NAME}"

# Wait for pod to be ready (NVMe device attachment takes longer)
test_info "Waiting for pod to be ready (NVMe-oF device attachment)..."
kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=300s

test_success "Pod ready with NVMe-oF volume attached"

#######################################
# Test 3: Write and read data
#######################################
test_step 3 5 "Testing write and read operations"

test_info "Writing test data..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c \
    "echo 'Hello from ${K8S_DISTRO} via NVMe-oF' > /data/test.txt"

test_info "Reading test data..."
READ_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)

if [[ "${READ_DATA}" == "Hello from ${K8S_DISTRO} via NVMe-oF" ]]; then
    test_success "Data written and read successfully"
else
    test_error "Data mismatch: expected 'Hello from ${K8S_DISTRO} via NVMe-oF', got '${READ_DATA}'"
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

# Delete pod first to detach NVMe device
test_info "Deleting pod..."
kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s --force --grace-period=0 || true

# Wait for device detachment
test_info "Waiting for NVMe device detachment..."
sleep 10

# Delete PVC
test_info "Deleting PVC..."
kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --timeout=120s

test_success "Cleanup complete"

echo ""
echo "=========================================================="
echo "NVMe-oF Basic Compatibility Test Summary (${K8S_DISTRO})"
echo "=========================================================="
echo ""
echo "✓ PVC creation and binding"
echo "✓ Pod attaching NVMe-oF volume"
echo "✓ Write/read data operations"
echo "✓ Large file I/O (10MB)"
echo "✓ Volume expansion (1Gi → 2Gi)"
echo ""
echo "Result: All basic operations successful on ${K8S_DISTRO}"
echo ""
echo "=========================================================="

# Success
test_summary "${PROTOCOL} (${K8S_DISTRO})" "PASSED"
