#!/bin/bash
# NVMe-oF PVC Lifecycle Test
# Tests PVC creation, binding, and deletion WITHOUT pod attachment
# This isolates the CSI controller provisioning/deletion path
# 
# Purpose: Verify that PVC cleanup properly deletes TrueNAS zvols and NVMe-oF subsystems
# Bug context: User reported "cleanup unable to delete dataset" on rke2
#
# NOTE: NVMe-oF uses WaitForFirstConsumer binding mode, so PVC won't bind without a pod.
# This test creates a temporary pod just to trigger binding, then deletes it before
# testing the PVC deletion path.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF"
PVC_NAME="test-pvc-lifecycle-nvmeof"
POD_NAME="test-pod-lifecycle-nvmeof"
TEST_TAGS="lifecycle,nvmeof"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF PVC Lifecycle Test"
echo "========================================"
echo ""
echo "This test verifies PVC create/bind/delete behavior for NVMe-oF."
echo "A temporary pod is used to trigger binding (WaitForFirstConsumer),"
echo "then deleted to test the PVC deletion path in isolation."
echo ""

# Configure test with 7 total steps:
# verify_cluster, deploy_driver, wait_for_driver, create_pvc, 
# trigger_binding_with_pod, delete_pod_only, delete_pvc_directly
set_test_steps 7

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping NVMe-oF PVC lifecycle test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

#######################################
# Cleanup function for PVC lifecycle test
#######################################
cleanup_pvc_lifecycle_test() {
    local pvc_name=$1
    local pod_name=$2
    
    echo ""
    test_info "Cleaning up PVC lifecycle test resources..."
    
    # Delete pod if it still exists
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    
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
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_pvc_lifecycle_test "${PVC_NAME}" "${POD_NAME}"; test_summary "${PROTOCOL} PVC Lifecycle" "FAILED"; exit 1' ERR

#######################################
# Create PVC (won't bind until pod is created due to WaitForFirstConsumer)
#######################################
create_pvc_nvmeof() {
    local pvc_name=$1
    
    start_test_timer "create_pvc_nvmeof"
    test_step "Creating PVC: ${pvc_name} (WaitForFirstConsumer)"
    
    # Create PVC manifest inline
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc_name}
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF
    
    test_info "PVC created (will bind when pod is scheduled)"
    
    # Verify PVC exists and is pending
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "unknown")
    test_info "PVC status: ${pvc_status}"
    
    if [[ "${pvc_status}" != "Pending" ]]; then
        test_warning "Expected PVC to be Pending (WaitForFirstConsumer), got: ${pvc_status}"
    fi
    
    stop_test_timer "create_pvc_nvmeof" "PASSED"
}

#######################################
# Create a minimal pod to trigger PVC binding
#######################################
trigger_binding_with_pod() {
    local pvc_name=$1
    local pod_name=$2
    
    start_test_timer "trigger_binding_with_pod"
    test_step "Creating temporary pod to trigger PVC binding"
    
    # Create minimal pod that uses the PVC
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
spec:
  containers:
    - name: test
      image: busybox:1.36
      command: ["sleep", "infinity"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${pvc_name}
EOF
    
    test_info "Pod created, waiting for it to be ready (this triggers PVC binding)..."
    
    # Wait for pod to be ready
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        stop_test_timer "trigger_binding_with_pod" "FAILED"
        test_error "Pod failed to become ready"
        
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Node Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
            --tail=100 || true
        
        false
    fi
    
    test_success "Pod is ready, PVC should now be bound"
    
    # Verify PVC is bound
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    if [[ "${pvc_status}" != "Bound" ]]; then
        test_error "Expected PVC to be Bound, got: ${pvc_status}"
        stop_test_timer "trigger_binding_with_pod" "FAILED"
        false
    fi
    
    # Get PV details
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    test_info "Created PV: ${pv_name}"
    
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    test_info "Volume handle (TrueNAS zvol): ${volume_handle}"
    
    echo ""
    echo "=== PVC Details ==="
    kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml
    
    echo ""
    echo "=== PV Details ==="
    kubectl get pv "${pv_name}" -o yaml
    
    stop_test_timer "trigger_binding_with_pod" "PASSED"
}

#######################################
# Delete pod only, leaving PVC intact
#######################################
delete_pod_only() {
    local pod_name=$1
    local pvc_name=$2
    
    start_test_timer "delete_pod_only"
    test_step "Deleting pod (leaving PVC intact for isolated deletion test)"
    
    # Delete the pod
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --timeout=60s
    
    test_success "Pod deleted"
    
    # Verify PVC is still bound (should remain bound after pod deletion)
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    test_info "PVC status after pod deletion: ${pvc_status}"
    
    if [[ "${pvc_status}" != "Bound" ]]; then
        test_warning "Expected PVC to remain Bound after pod deletion, got: ${pvc_status}"
    else
        test_success "PVC remains bound after pod deletion"
    fi
    
    # Give the system a moment to stabilize
    sleep 5
    
    stop_test_timer "delete_pod_only" "PASSED"
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
    
    local timeout=120  # NVMe-oF cleanup may take longer (subsystem deletion)
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
        test_success "PV deleted successfully in ${elapsed}s (TrueNAS zvol/subsystem cleanup completed)"
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
    
    # CRITICAL: Verify the zvol was actually deleted from TrueNAS
    # This is the key test - PV deletion alone doesn't prove backend cleanup
    echo ""
    test_info "Verifying zvol was deleted from TrueNAS backend..."
    if ! verify_truenas_deletion "${volume_handle}" 30; then
        test_error "TrueNAS zvol still exists! CSI DeleteVolume did not clean up backend."
        echo ""
        echo "=== Controller Logs (DeleteVolume) ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 | grep -i -E "(delete|volume|zvol|subsystem)" || true
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    test_success "TrueNAS zvol confirmed deleted - backend cleanup verified!"
    
    stop_test_timer "delete_pvc_directly" "PASSED"
}

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured
echo ""
test_info "Checking if NVMe-oF is configured on TrueNAS..."

# Create a temporary PVC to check configuration
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nvmeof-config-check
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF

# Wait a moment for controller to process
sleep 5

# Check controller logs for port configuration error
logs=$(kubectl logs -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    --tail=20 2>/dev/null || true)

if grep -q "No TCP NVMe-oF port" <<< "$logs"; then
    test_warning "NVMe-oF ports not configured on TrueNAS server"
    test_warning "Skipping NVMe-oF PVC lifecycle test - this is expected if NVMe-oF is not set up"
    kubectl delete pvc nvmeof-config-check -n "${TEST_NAMESPACE}" --ignore-not-found=true
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    test_summary "${PROTOCOL} PVC Lifecycle" "SKIPPED"
    exit 0
fi

# Clean up config check PVC
kubectl delete pvc nvmeof-config-check -n "${TEST_NAMESPACE}" --ignore-not-found=true
wait_for_resource_deleted "pvc" "nvmeof-config-check" "${TEST_NAMESPACE}" 30 || true

test_success "NVMe-oF is configured, proceeding with lifecycle test"

# Continue with the actual test
create_pvc_nvmeof "${PVC_NAME}"
trigger_binding_with_pod "${PVC_NAME}" "${POD_NAME}"
delete_pod_only "${POD_NAME}" "${PVC_NAME}"
delete_pvc_directly "${PVC_NAME}"

# Final cleanup (namespace only, PVC already deleted)
echo ""
test_info "Final cleanup..."
kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true

# Success
test_summary "${PROTOCOL} PVC Lifecycle" "PASSED"
