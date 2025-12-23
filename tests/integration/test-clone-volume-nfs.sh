#!/bin/bash
# NFS Volume Clone Integration Test
# Tests creating a PVC clone from an existing PVC (volume-to-volume cloning)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Volume Clone"
SOURCE_PVC_NAME="test-pvc-source-nfs"
SOURCE_POD_NAME="test-pod-source-nfs"
CLONE_PVC_NAME="test-pvc-clone-nfs"
CLONE_POD_NAME="test-pod-clone-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Check if test should be skipped based on tags
if should_skip_test "clone,nfs"; then
    echo "Skipping NFS Volume Clone test (tag-based skip)"
    exit 0
fi

echo "========================================"
echo "TrueNAS CSI - NFS Volume Clone Test"
echo "========================================"

# Configure test with 9 total steps
set_test_steps 9

# Trap errors and cleanup
trap 'save_diagnostic_logs "nfs-clone" "${CLONE_POD_NAME}" "${CLONE_PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${CLONE_POD_NAME}" "${CLONE_PVC_NAME}"; cleanup_clone_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Test clone data persistence
#######################################
test_clone_restore() {
    local clone_pvc_name=$1
    local clone_pod_name=$2
    local mount_path=$3
    local expected_content=$4
    
    test_step "Testing volume clone data: ${clone_pvc_name}"
    
    # Create PVC from source volume
    kubectl apply -f "${MANIFEST_DIR}/pvc-clone-from-volume-nfs.yaml" -n "${TEST_NAMESPACE}"
    test_info "Clone PVC created, waiting for provisioning..."
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for clone PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${clone_pvc_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        test_error "Clone PVC failed to bind"
        echo ""
        echo "=== Clone PVC Status ==="
        kubectl describe pvc "${clone_pvc_name}" -n "${TEST_NAMESPACE}" || true
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 || true
        return 1
    fi
    
    test_success "Clone PVC is bound"
    
    # Create test pod to mount cloned volume
    echo ""
    test_info "Creating pod to mount cloned volume: ${clone_pod_name}"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${clone_pod_name}
spec:
  containers:
  - name: test-container
    image: public.ecr.aws/docker/library/busybox:latest
    command: ["sleep", "3600"]
    volumeMounts:
    - name: test-volume
      mountPath: ${mount_path}
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${clone_pvc_name}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for clone pod to be ready (timeout: ${TIMEOUT_POD})..."
    if ! kubectl wait --for=condition=Ready pod/"${clone_pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Clone pod failed to become ready"
        echo ""
        echo "=== Clone Pod Status ==="
        kubectl describe pod "${clone_pod_name}" -n "${TEST_NAMESPACE}" || true
        return 1
    fi
    
    test_success "Clone pod is ready"
    
    # Verify data from source volume is present in clone
    echo ""
    test_info "Verifying cloned data is present..."
    
    if ! pod_file_exists "${clone_pod_name}" "${TEST_NAMESPACE}" "${mount_path}/test.txt"; then
        test_error "CRITICAL: test.txt does not exist in clone!"
        echo ""
        echo "=== Clone Volume Contents ==="
        kubectl exec "${clone_pod_name}" -n "${TEST_NAMESPACE}" -- ls -la "${mount_path}/" || true
        return 1
    fi
    
    local content
    if ! content=$(kubectl exec "${clone_pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test.txt" 2>/dev/null); then
        test_error "Failed to read test.txt from clone!"
        return 1
    fi
    
    test_info "Retrieved: '${content}', Expected: '${expected_content}'"
    
    if [[ "${content}" == "${expected_content}" ]]; then
        test_success "Clone data verified: ${content}"
    else
        test_error "Data mismatch: expected '${expected_content}', got '${content}'"
        return 1
    fi
    
    # Verify we can write new data to clone (proves it's independent)
    echo ""
    test_info "Writing new data to cloned volume (testing independence)..."
    kubectl exec "${clone_pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Data written to clone' > ${mount_path}/clone-data.txt"
    
    local clone_content
    clone_content=$(kubectl exec "${clone_pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/clone-data.txt")
    
    if [[ "${clone_content}" == "Data written to clone" ]]; then
        test_success "Write to clone successful - clone is independent"
    else
        test_error "Failed to write to clone"
        return 1
    fi
    
    # Verify original volume is unaffected
    echo ""
    test_info "Verifying source volume is unaffected..."
    
    if kubectl exec "${SOURCE_POD_NAME}" -n "${TEST_NAMESPACE}" -- test -f "${mount_path}/clone-data.txt" 2>/dev/null; then
        test_error "CRITICAL: Clone data appeared in source volume - volumes are not independent!"
        return 1
    fi
    
    test_success "Source volume is unaffected - clone is truly independent"
}

#######################################
# Cleanup clone test resources
#######################################
cleanup_clone_test() {
    test_step "Cleaning up clone test resources"
    
    # Delete pods first
    test_info "Deleting pods..."
    kubectl delete pod "${SOURCE_POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    kubectl delete pod "${CLONE_POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVCs
    test_info "Deleting PVCs..."
    kubectl delete pvc "${CLONE_PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    kubectl delete pvc "${SOURCE_PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for PVs to be deleted
    test_info "Waiting for PVs to be deleted..."
    for i in {1..60}; do
        REMAINING_PVS=$(kubectl get pv --no-headers 2>/dev/null | grep -c "${TEST_NAMESPACE}" || echo "0")
        if [[ "${REMAINING_PVS}" == "0" ]]; then
            test_success "All PVs deleted successfully"
            break
        fi
        if [[ $i == 60 ]]; then
            test_warning "Some PVs still exist after 60 seconds"
            kubectl get pv | grep "${TEST_NAMESPACE}" || true
        fi
        sleep 1
    done
    
    test_success "Cleanup complete"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver

# Create source PVC
create_pvc "${MANIFEST_DIR}/pvc-source-nfs.yaml" "${SOURCE_PVC_NAME}"

# Create source pod and write test data
create_test_pod "${MANIFEST_DIR}/pod-source-nfs.yaml" "${SOURCE_POD_NAME}"

# Write test data to source volume
test_step "Writing test data to source volume"
kubectl exec "${SOURCE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Source Volume Data' > /data/test.txt && sync"
test_success "Test data written to source volume"

# Test cloning and verify data
if ! test_clone_restore "${CLONE_PVC_NAME}" "${CLONE_POD_NAME}" "/data" "Source Volume Data"; then
    exit 1
fi

# Verify metrics
verify_metrics

# Success
cleanup_clone_test
test_summary "${PROTOCOL}" "PASSED"
