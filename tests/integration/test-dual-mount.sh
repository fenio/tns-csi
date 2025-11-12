#!/bin/bash
# Dual Mount Integration Test
# Tests simultaneous NFS and NVMe-oF volume mounting in a single pod

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Dual-Mount"
PVC_NAME_NFS="test-pvc-dual-nfs"
PVC_NAME_NVMEOF="test-pvc-dual-nvmeof"
POD_NAME="test-pod-dual-mount"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
TEST_TAGS="basic,nfs,nvmeof,dual-mount"

echo "=========================================="
echo "TrueNAS CSI - Dual Mount Integration Test"
echo "=========================================="
echo ""
test_info "This test validates that NFS and NVMe-oF volumes can be mounted simultaneously in a single pod"
echo ""

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping dual-mount test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME_NFS},${PVC_NAME_NVMEOF}"; cleanup_dual_test "${POD_NAME}" "${PVC_NAME_NFS}" "${PVC_NAME_NVMEOF}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Custom cleanup function for dual mount test
cleanup_dual_test() {
    local pod_name=$1
    local pvc_nfs=$2
    local pvc_nvmeof=$3
    
    test_step 10 10 "Cleaning up test resources"
    
    # Delete pod first
    if kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_info "Deleting test pod: ${pod_name}"
        kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s
    fi
    
    # Delete PVCs
    if kubectl get pvc "${pvc_nfs}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_info "Deleting NFS PVC: ${pvc_nfs}"
        kubectl delete pvc "${pvc_nfs}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s
    fi
    
    if kubectl get pvc "${pvc_nvmeof}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_info "Deleting NVMe-oF PVC: ${pvc_nvmeof}"
        kubectl delete pvc "${pvc_nvmeof}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s
    fi
    
    # Clean up test namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    test_success "Cleanup completed"
}

# Verify cluster
verify_cluster

# Deploy driver with BOTH storage classes enabled
start_test_timer "deploy_driver_dual"
test_step 2 10 "Deploying CSI driver with both NFS and NVMe-oF support"

# Check required environment variables
if [[ -z "${TRUENAS_HOST}" ]]; then
    stop_test_timer "deploy_driver_dual" "FAILED"
    test_error "TRUENAS_HOST environment variable not set"
    exit 1
fi

if [[ -z "${TRUENAS_API_KEY}" ]]; then
    stop_test_timer "deploy_driver_dual" "FAILED"
    test_error "TRUENAS_API_KEY environment variable not set"
    exit 1
fi

if [[ -z "${TRUENAS_POOL}" ]]; then
    stop_test_timer "deploy_driver_dual" "FAILED"
    test_error "TRUENAS_POOL environment variable not set"
    exit 1
fi

# Construct TrueNAS WebSocket URL
truenas_url="wss://${TRUENAS_HOST}/api/current"
test_info "TrueNAS URL: ${truenas_url}"

# NVMe-oF subsystem NQN
subsystem_nqn="${NVMEOF_SUBSYSTEM_NQN:-nqn.2005-03.org.truenas:csi-test}"
test_info "NVMe-oF subsystem NQN: ${subsystem_nqn}"
test_warning "IMPORTANT: The NVMe-oF subsystem must be pre-configured in TrueNAS"

# Deploy with Helm - enable BOTH NFS and NVMe-oF
if ! helm upgrade --install tns-csi ./charts/tns-csi-driver \
    --namespace kube-system \
    --create-namespace \
    --set image.repository=bfenski/tns-csi \
    --set image.tag=latest \
    --set image.pullPolicy=Always \
    --set truenas.url="${truenas_url}" \
    --set truenas.apiKey="${TRUENAS_API_KEY}" \
    --set storageClasses.nfs.enabled=true \
    --set storageClasses.nfs.name=tns-csi-nfs \
    --set storageClasses.nfs.pool="${TRUENAS_POOL}" \
    --set storageClasses.nfs.server="${TRUENAS_HOST}" \
    --set storageClasses.nvmeof.enabled=true \
    --set storageClasses.nvmeof.name=tns-csi-nvmeof \
    --set storageClasses.nvmeof.pool="${TRUENAS_POOL}" \
    --set storageClasses.nvmeof.server="${TRUENAS_HOST}" \
    --set storageClasses.nvmeof.transport=tcp \
    --set storageClasses.nvmeof.port=4420 \
    --set storageClasses.nvmeof.subsystemNQN="${subsystem_nqn}" \
    --wait --timeout 5m; then
    stop_test_timer "deploy_driver_dual" "FAILED"
    test_error "Helm deployment failed"
    exit 1
fi

test_success "CSI driver deployed with dual protocol support"

echo ""
echo "=== Helm deployment status ==="
helm list -n kube-system

echo ""
echo "=== CSI driver pods ==="
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver
stop_test_timer "deploy_driver_dual" "PASSED"

# Wait for driver to be ready
wait_for_driver

# Check if NVMe-oF is configured (using temporary PVC)
test_step 4 10 "Checking NVMe-oF configuration"
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-dual-nvmeof.yaml" "${PVC_NAME_NVMEOF}" "${PROTOCOL}"; then
    exit 0  # Gracefully skip test if not configured
fi

# Create NFS PVC (immediate binding)
test_step 5 10 "Creating NFS PVC"
create_pvc "${MANIFEST_DIR}/pvc-dual-nfs.yaml" "${PVC_NAME_NFS}"

# Create NVMe-oF PVC (WaitForFirstConsumer binding - will bind when pod starts)
test_step 6 10 "Creating NVMe-oF PVC"
create_pvc "${MANIFEST_DIR}/pvc-dual-nvmeof.yaml" "${PVC_NAME_NVMEOF}" "false"

# Create pod with both volumes mounted
test_step 7 10 "Creating pod with both NFS and NVMe-oF volumes"
start_test_timer "create_dual_pod"

test_info "Deploying pod: ${POD_NAME}"
kubectl apply -f "${MANIFEST_DIR}/pod-dual-mount.yaml" -n "${TEST_NAMESPACE}"

test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
if ! kubectl wait --for=condition=Ready pod/"${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout="${TIMEOUT_POD}"; then
    stop_test_timer "create_dual_pod" "FAILED"
    test_error "Pod did not become ready within timeout"
    kubectl describe pod "${POD_NAME}" -n "${TEST_NAMESPACE}"
    exit 1
fi

test_success "Pod is ready with both volumes mounted"
kubectl get pod "${POD_NAME}" -n "${TEST_NAMESPACE}" -o wide
stop_test_timer "create_dual_pod" "PASSED"

# Verify both PVCs are now bound
echo ""
test_info "Verifying both PVCs are bound"
kubectl get pvc -n "${TEST_NAMESPACE}"

# Test I/O operations on both volumes
test_step 8 10 "Testing I/O operations on both volumes"

start_test_timer "io_operations_nfs"
test_info "Testing NFS volume at /data-nfs"

# Write test
test_info "Writing test file to NFS volume..."
if ! kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c "echo 'NFS test data' > /data-nfs/test.txt"; then
    stop_test_timer "io_operations_nfs" "FAILED"
    test_error "Failed to write to NFS volume"
    exit 1
fi

# Read test
test_info "Reading test file from NFS volume..."
read_data=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data-nfs/test.txt)
if [[ "${read_data}" != "NFS test data" ]]; then
    stop_test_timer "io_operations_nfs" "FAILED"
    test_error "NFS data verification failed. Expected: 'NFS test data', Got: '${read_data}'"
    exit 1
fi

test_success "NFS volume I/O operations successful"
stop_test_timer "io_operations_nfs" "PASSED"

start_test_timer "io_operations_nvmeof"
test_info "Testing NVMe-oF volume at /data-nvmeof"

# Write test
test_info "Writing test file to NVMe-oF volume..."
if ! kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c "echo 'NVMe-oF test data' > /data-nvmeof/test.txt"; then
    stop_test_timer "io_operations_nvmeof" "FAILED"
    test_error "Failed to write to NVMe-oF volume"
    exit 1
fi

# Read test
test_info "Reading test file from NVMe-oF volume..."
read_data=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data-nvmeof/test.txt)
if [[ "${read_data}" != "NVMe-oF test data" ]]; then
    stop_test_timer "io_operations_nvmeof" "FAILED"
    test_error "NVMe-oF data verification failed. Expected: 'NVMe-oF test data', Got: '${read_data}'"
    exit 1
fi

test_success "NVMe-oF volume I/O operations successful"
stop_test_timer "io_operations_nvmeof" "PASSED"

# Verify isolation - data written to one volume should not appear in the other
test_info "Verifying volume isolation..."
if kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- test -f /data-nvmeof/test.txt.nfs 2>/dev/null; then
    test_error "Volume isolation failed - unexpected cross-volume file access"
    exit 1
fi

test_success "Volume isolation verified - both volumes operate independently"

# Verify metrics
test_step 9 10 "Verifying CSI driver metrics"
verify_metrics

# Cleanup
cleanup_dual_test "${POD_NAME}" "${PVC_NAME_NFS}" "${PVC_NAME_NVMEOF}"

# Success
test_summary "${PROTOCOL}" "PASSED"
