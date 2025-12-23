#!/bin/bash
# NFS Detached Snapshot Integration Test
# Tests creating detached clones from snapshots that can outlive the original snapshot
#
# This test verifies:
# 1. Create a PVC and write data to it
# 2. Create a VolumeSnapshot
# 3. Create a new PVC from snapshot using detached StorageClass
# 4. Verify data is present in the detached clone
# 5. Delete the original snapshot (should succeed because clone is detached/promoted)
# 6. Verify the detached volume still works after snapshot deletion

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Detached Snapshot"
PVC_NAME="test-pvc-nfs"
POD_NAME="test-pod-nfs"
SNAPSHOT_NAME="test-snapshot-nfs"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-nfs"
PVC_DETACHED="test-pvc-detached-nfs"
POD_DETACHED="test-pod-detached-nfs"
STORAGE_CLASS_DETACHED="tns-csi-nfs-detached"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "========================================"
echo "TrueNAS CSI - NFS Detached Snapshot Test"
echo "========================================"

# Configure test with 11 total steps
set_test_steps 11

# Trap errors and cleanup
trap 'save_diagnostic_logs "nfs-detached-snapshot" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_detached_snapshot_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create VolumeSnapshot from PVC
#######################################
create_snapshot() {
    local snapshot_manifest=$1
    local snapshot_name=$2
    
    test_step "Creating VolumeSnapshot: ${snapshot_name}"
    
    # Apply VolumeSnapshotClass first
    kubectl apply -f "${MANIFEST_DIR}/volumesnapshotclass-nfs.yaml"
    test_success "VolumeSnapshotClass created"
    
    # Apply VolumeSnapshot
    kubectl apply -f "${snapshot_manifest}" -n "${TEST_NAMESPACE}"
    
    # Wait for snapshot to be ready
    echo ""
    test_info "Waiting for snapshot to be ready (timeout: ${TIMEOUT_PVC})..."
    
    local retries=0
    local max_retries=60
    while [[ $retries -lt $max_retries ]]; do
        local ready_to_use
        ready_to_use=$(kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
        
        if [[ "${ready_to_use}" == "true" ]]; then
            test_success "Snapshot is ready"
            break
        fi
        
        sleep 2
        retries=$((retries + 1))
    done
    
    if [[ $retries -eq $max_retries ]]; then
        test_error "Timeout waiting for snapshot to be ready"
        
        echo ""
        echo "=== VolumeSnapshot Status ==="
        kubectl describe volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 || true
        
        return 1
    fi
    
    # Show snapshot details
    echo ""
    echo "=== VolumeSnapshot Details ==="
    kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" -o yaml
    
    # Get snapshot content name
    local snapshot_content
    snapshot_content=$(kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.boundVolumeSnapshotContentName}')
    test_info "Created VolumeSnapshotContent: ${snapshot_content}"
}

#######################################
# Create detached StorageClass
#######################################
create_detached_storage_class() {
    test_step "Creating detached StorageClass: ${STORAGE_CLASS_DETACHED}"
    
    # Substitute environment variables
    envsubst < "${MANIFEST_DIR}/storageclass-nfs-detached.yaml" | kubectl apply -f -
    
    test_success "Detached StorageClass created"
}

#######################################
# Create PVC from snapshot using detached StorageClass and test data persistence
#######################################
test_detached_snapshot_restore() {
    local pvc_manifest=$1
    local pvc_name=$2
    local pod_name=$3
    local mount_path=$4
    local expected_content=$5
    
    test_step "Testing detached snapshot restore: ${pvc_name}"
    
    # Show controller logs BEFORE creating PVC from snapshot
    echo ""
    echo "=== Controller Logs BEFORE detached PVC creation ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=50 || true
    
    # Create PVC from snapshot using detached StorageClass
    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}"
    test_info "PVC from snapshot (detached) created, waiting for provisioning..."
    
    # Wait a moment for the provisioner to pick up the PVC
    sleep 5
    
    # Show controller logs DURING PVC provisioning attempt
    echo ""
    echo "=== Controller Logs DURING detached PVC provisioning (5s after creation) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=100 || true
    
    echo ""
    echo "=== PVC Status ==="
    kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${pvc_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"
    
    test_success "Detached PVC from snapshot is bound"
    
    # Create test pod manifest on the fly
    echo ""
    test_info "Creating pod to mount detached cloned volume: ${pod_name}"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
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
      claimName: ${pvc_name}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"
    
    test_success "Pod is ready"
    
    # Verify data from original volume is present
    echo ""
    test_info "Verifying snapshot data is present in detached clone..."
    local content
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/test.txt"; then
        test_error "CRITICAL: test.txt does not exist in detached snapshot!"
        return 1
    fi
    
    if ! content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test.txt" 2>/dev/null); then
        test_error "Failed to read test.txt from detached snapshot!"
        test_error "Error: ${content}"
        return 1
    fi
    
    test_info "Retrieved: '${content}', Expected: '${expected_content}'"
    
    if [[ "${content}" == "${expected_content}" ]]; then
        test_success "Detached snapshot data verified: ${content}"
    else
        test_error "Data mismatch: expected '${expected_content}', got '${content}'"
        return 1
    fi
    
    # Write new data to detached cloned volume
    echo ""
    test_info "Writing new data to detached cloned volume..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Data written to detached cloned volume' > ${mount_path}/detached-data.txt"
    
    local new_content
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/detached-data.txt"; then
        test_error "CRITICAL: detached-data.txt was not created!"
        return 1
    fi
    
    if ! new_content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/detached-data.txt" 2>/dev/null); then
        test_error "Failed to read detached-data.txt!"
        test_error "Error: ${new_content}"
        return 1
    fi
    
    test_info "Retrieved: '${new_content}'"
    
    if [[ "${new_content}" == "Data written to detached cloned volume" ]]; then
        test_success "Write to detached cloned volume successful"
    else
        test_error "Failed to write to detached cloned volume (got: '${new_content}')"
        return 1
    fi
}

#######################################
# Test that detached clone survives snapshot deletion
#######################################
test_snapshot_deletion() {
    local snapshot_name=$1
    local pod_name=$2
    local mount_path=$3
    
    test_step "Testing that detached clone survives snapshot deletion"
    
    # Delete the original snapshot
    test_info "Deleting original VolumeSnapshot: ${snapshot_name}"
    kubectl delete volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" --timeout=120s
    
    test_success "VolumeSnapshot deleted successfully"
    
    # Wait a moment for any potential cascading effects
    sleep 5
    
    # Verify the detached clone still works
    test_info "Verifying detached clone still works after snapshot deletion..."
    
    # Check pod is still running
    local pod_status
    pod_status=$(kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null)
    if [[ "${pod_status}" != "Running" ]]; then
        test_error "Pod is no longer running after snapshot deletion (status: ${pod_status})"
        return 1
    fi
    test_success "Pod is still running"
    
    # Check we can still read the original data
    local content
    if ! content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test.txt" 2>/dev/null); then
        test_error "Failed to read test.txt after snapshot deletion!"
        return 1
    fi
    test_success "Can still read original data: ${content}"
    
    # Check we can still read data written to detached clone
    local detached_content
    if ! detached_content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/detached-data.txt" 2>/dev/null); then
        test_error "Failed to read detached-data.txt after snapshot deletion!"
        return 1
    fi
    test_success "Can still read detached clone data: ${detached_content}"
    
    # Write new data after snapshot deletion
    test_info "Writing new data after snapshot deletion..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Data written after snapshot deletion' > ${mount_path}/post-deletion-data.txt"
    
    local new_content
    if ! new_content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/post-deletion-data.txt" 2>/dev/null); then
        test_error "Failed to write/read new data after snapshot deletion!"
        return 1
    fi
    
    test_success "Successfully wrote new data after snapshot deletion: ${new_content}"
    test_success "Detached clone is fully independent from deleted snapshot!"
}

#######################################
# Cleanup detached snapshot test resources
#######################################
cleanup_detached_snapshot_test() {
    test_step "Cleaning up detached snapshot test resources"
    
    # Delete pods first
    test_info "Deleting pods..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Pod ${POD_NAME} deletion timed out or failed"
    }
    kubectl delete pod "${POD_DETACHED}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Pod ${POD_DETACHED} deletion timed out or failed"
    }
    
    # Delete PVCs (this should trigger PV deletion)
    test_info "Deleting PVCs..."
    kubectl delete pvc "${PVC_DETACHED}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "PVC ${PVC_DETACHED} deletion timed out after 60s - continuing anyway"
    }
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "PVC ${PVC_NAME} deletion timed out after 60s - continuing anyway"
    }
    
    # Give CSI driver time to process DeleteVolume
    test_info "Waiting 10 seconds for CSI DeleteVolume to process..."
    sleep 10
    
    # Delete snapshot (may already be deleted by test)
    test_info "Deleting VolumeSnapshot (if still exists)..."
    kubectl delete volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "VolumeSnapshot ${SNAPSHOT_NAME} deletion timed out or failed"
    }
    
    # Delete VolumeSnapshotClass
    test_info "Deleting VolumeSnapshotClass..."
    kubectl delete volumesnapshotclass "${SNAPSHOT_CLASS_NAME}" --ignore-not-found=true || true
    
    # Delete detached StorageClass
    test_info "Deleting detached StorageClass..."
    kubectl delete storageclass "${STORAGE_CLASS_DETACHED}" --ignore-not-found=true || true
    
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
    
    # Additional wait for TrueNAS backend cleanup
    test_info "Waiting for TrueNAS backend cleanup (15 seconds)..."
    sleep 15
    
    test_success "Cleanup complete"
}

# Run test steps
verify_cluster
deploy_driver "nfs" --set snapshots.enabled=true --set snapshots.volumeSnapshotClass.create=true
wait_for_driver
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${POD_NAME}"
test_io_operations "${POD_NAME}" "/data" "filesystem"
create_snapshot "${MANIFEST_DIR}/volumesnapshot-nfs.yaml" "${SNAPSHOT_NAME}"
create_detached_storage_class
test_detached_snapshot_restore "${MANIFEST_DIR}/pvc-detached-from-snapshot-nfs.yaml" "${PVC_DETACHED}" "${POD_DETACHED}" "/data" "CSI Test Data"
test_snapshot_deletion "${SNAPSHOT_NAME}" "${POD_DETACHED}" "/data"
verify_metrics

# Success
cleanup_detached_snapshot_test
test_summary "${PROTOCOL}" "PASSED"
