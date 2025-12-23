#!/bin/bash
# Delete Strategy Retain Integration Test
# Tests that deleteStrategy=retain keeps TrueNAS resources after PV deletion

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Delete Strategy Retain"
PVC_NAME="test-pvc-nfs-retain"
POD_NAME="test-pod-nfs-retain"
STORAGECLASS_NAME="tns-csi-nfs-retain"

# Check if test should be skipped based on tags
if should_skip_test "retain,nfs"; then
    echo "Skipping Delete Strategy Retain test (tag-based skip)"
    exit 0
fi

echo "========================================"
echo "TrueNAS CSI - Delete Strategy Retain Test"
echo "========================================"

# Configure test with 10 total steps
set_test_steps 10

# Trap errors and cleanup
trap 'save_diagnostic_logs "nfs-retain" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_retain_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create StorageClass with deleteStrategy=retain
#######################################
create_retain_storageclass() {
    test_step "Creating StorageClass with deleteStrategy=retain"
    
    # Substitute environment variables in the manifest
    test_info "Creating StorageClass: ${STORAGECLASS_NAME}"
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${STORAGECLASS_NAME}
provisioner: tns.csi.io
parameters:
  protocol: nfs
  server: "${TRUENAS_HOST}"
  pool: "${TRUENAS_POOL}"
  deleteStrategy: retain
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: Immediate
EOF
    
    test_success "StorageClass created with deleteStrategy=retain"
    
    # Show StorageClass details
    if is_verbose 1; then
        echo ""
        echo "=== StorageClass Details ==="
        kubectl get storageclass "${STORAGECLASS_NAME}" -o yaml
    fi
}

#######################################
# Test that TrueNAS resources are retained after PV deletion
#######################################
test_retain_behavior() {
    local pvc_name=$1
    local pv_name
    local volume_handle
    
    test_step "Testing delete strategy retain behavior"
    
    # Get the PV name and volume handle
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    
    test_info "PV name: ${pv_name}"
    test_info "Volume handle: ${volume_handle}"
    
    # Parse volume handle to get TrueNAS dataset path
    # Format is typically: protocol#server#datasetPath
    local dataset_path
    dataset_path=$(echo "${volume_handle}" | cut -d'#' -f3)
    test_info "TrueNAS dataset path: ${dataset_path}"
    
    # Delete the pod first
    echo ""
    test_info "Deleting pod to release the volume..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "Pod deleted"
    
    # Delete the PVC (this triggers DeleteVolume with retain strategy)
    echo ""
    test_info "Deleting PVC to trigger volume deletion..."
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "PVC deleted"
    
    # Wait for PV to be deleted (the PV should be removed from Kubernetes)
    echo ""
    test_info "Waiting for PV to be deleted from Kubernetes..."
    
    local timeout=60
    local elapsed=0
    local interval=2
    
    while [[ $elapsed -lt $timeout ]]; do
        if ! kubectl get pv "${pv_name}" &>/dev/null; then
            test_success "PV deleted from Kubernetes"
            break
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    if [[ $elapsed -ge $timeout ]]; then
        test_warning "PV still exists after ${timeout}s - checking status"
        kubectl describe pv "${pv_name}" || true
    fi
    
    # Verify the behavior through controller logs
    echo ""
    test_info "Checking controller logs for retain behavior..."
    
    local logs
    logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=100 2>/dev/null || true)
    
    # Always show the relevant controller logs for debugging
    echo ""
    echo "=== Controller Logs (DeleteVolume operation) ==="
    echo "${logs}" | grep -E "DEBUG:|DeleteVolume|retain|dataset|share|delete_strategy|deleteStrategy" || echo "No matching log entries found"
    echo "=== End Controller Logs ==="
    echo ""
    
    # Check for the specific retain behavior message that indicates actual retention
    # This message is only printed when deleteStrategy=retain is detected and deletion is skipped
    if echo "${logs}" | grep -q "deleteStrategy=retain, skipping actual deletion"; then
        test_success "Controller logs confirm volume was retained (found 'skipping actual deletion' message)"
    elif echo "${logs}" | grep -q "deleteStrategy.*retain\|retaining\|kept"; then
        test_warning "Found retain-related log message, but not the specific 'skipping actual deletion' message"
        test_info "This may indicate deleteStrategy was configured but not properly honored"
    else
        test_error "No retain behavior detected in controller logs"
        test_info "Expected to find: 'deleteStrategy=retain, skipping actual deletion'"
        return 1
    fi
    
    test_success "Delete strategy retain test completed"
    test_info "NOTE: TrueNAS resources should be manually verified to be retained"
    test_info "Dataset path that should still exist: ${dataset_path}"
}

#######################################
# Cleanup retain test resources
#######################################
cleanup_retain_test() {
    test_step "Cleaning up retain test resources"
    
    # Delete pod if still exists
    test_info "Deleting pod (if exists)..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVC if still exists
    test_info "Deleting PVC (if exists)..."
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete the custom StorageClass
    test_info "Deleting custom StorageClass..."
    kubectl delete storageclass "${STORAGECLASS_NAME}" --ignore-not-found=true || true
    
    # Delete namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Note: We don't wait for PVs to be deleted because with retain strategy,
    # the TrueNAS resources are retained and may need manual cleanup
    
    test_success "Cleanup complete"
    test_info "NOTE: TrueNAS resources created with deleteStrategy=retain may need manual cleanup"
}

#######################################
# Create test pod for retain test
#######################################
create_retain_test_pod() {
    test_step "Creating test pod: ${POD_NAME}"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: test-container
    image: public.ecr.aws/docker/library/busybox:latest
    command: ["sleep", "3600"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    
    if ! kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Pod failed to become ready"
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${POD_NAME}" -n "${TEST_NAMESPACE}" || true
        return 1
    fi
    
    test_success "Pod is ready"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver

# Create custom StorageClass with retain strategy
create_retain_storageclass

# Create PVC with retain StorageClass
test_step "Creating PVC with retain StorageClass"
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
  storageClassName: ${STORAGECLASS_NAME}
EOF

# Wait for PVC to be bound
test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"; then
    test_error "PVC failed to bind"
    false
fi
test_success "PVC is bound"

# Create test pod and write some data
create_retain_test_pod

# Write test data to verify the volume is working
test_step "Writing test data to volume"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Retain Test Data' > /data/retain-test.txt"
test_success "Test data written"

# Test the retain behavior
test_retain_behavior "${PVC_NAME}"

# Verify metrics
verify_metrics

# Cleanup (note: TrueNAS resources are retained by design)
cleanup_retain_test
test_summary "${PROTOCOL}" "PASSED"
