#!/bin/bash
# NFS PVC Lifecycle Test
# Tests PVC creation, binding, and deletion WITHOUT pod attachment
# This isolates the CSI controller provisioning/deletion path
# 
# Purpose: Verify that PVC cleanup properly deletes TrueNAS datasets
# Bug context: User reported "cleanup unable to delete dataset" on rke2

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS"
PVC_NAME="test-pvc-lifecycle-nfs"
TEST_TAGS="lifecycle,nfs"

echo "========================================"
echo "TrueNAS CSI - NFS PVC Lifecycle Test"
echo "========================================"
echo ""
echo "This test verifies PVC create/bind/delete WITHOUT pod attachment"
echo "to isolate CSI controller provisioning and deletion behavior."
echo ""

# Configure test with 5 total steps:
# verify_cluster, deploy_driver, wait_for_driver, create_and_verify_pvc, delete_pvc_directly
set_test_steps 5

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping NFS PVC lifecycle test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

#######################################
# Cleanup function for PVC lifecycle test
#######################################
cleanup_pvc_lifecycle_test() {
    local pvc_name=$1
    
    echo ""
    test_info "Cleaning up PVC lifecycle test resources..."
    
    # Delete PVC if it still exists
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    
    # Delete namespace
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Namespace deletion timed out, forcing..."
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    test_success "Cleanup completed"
}

# Trap errors and cleanup
trap 'show_diagnostic_logs "" "${PVC_NAME}"; cleanup_pvc_lifecycle_test "${PVC_NAME}"; test_summary "${PROTOCOL} PVC Lifecycle" "FAILED"; exit 1' ERR

#######################################
# Create PVC and wait for it to be bound (NFS binds immediately)
#######################################
create_and_verify_pvc() {
    local pvc_name=$1
    
    start_test_timer "create_and_verify_pvc"
    test_step "Creating and verifying PVC: ${pvc_name}"
    
    # Create PVC manifest inline for clarity
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc_name}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF
    
    test_info "PVC created, waiting for binding..."
    
    # Wait for PVC to be bound
    if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${pvc_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        stop_test_timer "create_and_verify_pvc" "FAILED"
        test_error "PVC failed to bind"
        
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 || true
        
        false
    fi
    
    test_success "PVC is bound"
    
    # Get PV name
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    test_info "Created PV: ${pv_name}"
    
    # Show PVC and PV details
    echo ""
    echo "=== PVC Details ==="
    kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml
    
    echo ""
    echo "=== PV Details ==="
    kubectl get pv "${pv_name}" -o yaml
    
    # Extract volume handle (TrueNAS dataset path)
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    test_info "Volume handle (TrueNAS dataset): ${volume_handle}"
    
    stop_test_timer "create_and_verify_pvc" "PASSED"
}

#######################################
# Delete PVC directly and verify cleanup
#######################################
delete_pvc_directly() {
    local pvc_name=$1
    
    start_test_timer "delete_pvc_directly"
    test_step "Deleting PVC directly: ${pvc_name}"
    
    # Get PV name before deletion
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}' 2>/dev/null || echo "")
    
    if [[ -z "${pv_name}" ]]; then
        test_error "Could not get PV name from PVC"
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    
    test_info "Associated PV: ${pv_name}"
    
    # Get volume handle for logging
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}' 2>/dev/null || echo "unknown")
    test_info "Volume handle to be deleted: ${volume_handle}"
    
    # Delete PVC directly (NOT via namespace deletion)
    echo ""
    test_info "Deleting PVC ${pvc_name}..."
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --timeout=60s
    
    test_success "PVC deletion command completed"
    
    # Wait for PV to be deleted (indicates successful backend cleanup)
    echo ""
    test_info "Waiting for PV ${pv_name} to be deleted (indicates TrueNAS cleanup)..."
    
    local timeout=90
    local elapsed=0
    local interval=2
    local pv_deleted=false
    
    while [[ $elapsed -lt $timeout ]]; do
        if ! kubectl get pv "${pv_name}" &>/dev/null; then
            pv_deleted=true
            break
        fi
        
        # Check PV status
        local pv_status
        pv_status=$(kubectl get pv "${pv_name}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "unknown")
        test_info "PV status: ${pv_status} (elapsed: ${elapsed}s)"
        
        # Check if PV is stuck in Released state (common issue)
        if [[ "${pv_status}" == "Released" ]]; then
            test_warning "PV is in Released state - checking for finalizer issues..."
            kubectl get pv "${pv_name}" -o jsonpath='{.metadata.finalizers}' || true
            echo ""
        fi
        
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    if [[ "${pv_deleted}" == "true" ]]; then
        test_success "PV deleted successfully in ${elapsed}s (TrueNAS dataset cleanup completed)"
    else
        test_error "PV deletion timed out after ${timeout}s"
        
        echo ""
        echo "=== PV Status (stuck) ==="
        kubectl describe pv "${pv_name}" || true
        
        echo ""
        echo "=== Controller Logs (last 200 lines) ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=200 || true
        
        echo ""
        echo "=== CSI Provisioner Sidecar Logs ==="
        local controller_pod
        controller_pod=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "${controller_pod}" ]]; then
            kubectl logs -n kube-system "${controller_pod}" -c csi-provisioner --tail=100 || true
        fi
        
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    
    # Verify PVC is also gone
    if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_warning "PVC still exists after PV deletion - this is unexpected"
    else
        test_success "PVC confirmed deleted"
    fi
    
    stop_test_timer "delete_pvc_directly" "PASSED"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver
create_and_verify_pvc "${PVC_NAME}"
delete_pvc_directly "${PVC_NAME}"

# Final cleanup (namespace only, PVC already deleted)
echo ""
test_info "Final cleanup..."
kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true

# Success
test_summary "${PROTOCOL} PVC Lifecycle" "PASSED"
