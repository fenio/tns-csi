#!/bin/bash
# Detached Clone Integration Test
# Tests that clones created with detachedVolumesFromSnapshots=true are independent
# of the parent volume (uses zfs send/receive instead of zfs clone)
#
# This test validates that:
# 1. Snapshot can be created from a volume
# 2. Clone can be restored from snapshot with detached mode
# 3. Original volume can be deleted without affecting the clone
# 4. Clone remains fully functional after parent deletion

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Detached Clone"
PVC_NAME="test-pvc-parent"
POD_NAME="test-pod-parent"
SNAPSHOT_NAME="test-snapshot-detached"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-detached"
PVC_CLONE_NAME="test-pvc-detached-clone"
POD_CLONE_NAME="test-pod-detached-clone"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Test steps: cluster, deploy, driver ready, create parent pvc, create parent pod,
# write data, create snapshot, create detached clone, verify clone data,
# delete parent volume, verify clone still works, cleanup
set_test_steps 12

echo "========================================"
echo "TrueNAS CSI - Detached Clone Test"
echo "========================================"
echo ""
echo "This test validates zfs send/receive based clones that are"
echo "independent of the parent volume."
echo ""

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_detached_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create VolumeSnapshotClass with detached mode
#######################################
create_detached_snapshot_class() {
    test_step "Creating VolumeSnapshotClass with detached mode"
    
    cat <<EOF | kubectl apply -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: ${SNAPSHOT_CLASS_NAME}
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  detachedVolumesFromSnapshots: "true"
EOF
    
    test_success "VolumeSnapshotClass created with detachedVolumesFromSnapshots=true"
    
    echo ""
    echo "=== VolumeSnapshotClass Details ==="
    kubectl get volumesnapshotclass "${SNAPSHOT_CLASS_NAME}" -o yaml
}

#######################################
# Create VolumeSnapshot from PVC
#######################################
create_snapshot() {
    test_step "Creating VolumeSnapshot: ${SNAPSHOT_NAME}"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${SNAPSHOT_NAME}
spec:
  volumeSnapshotClassName: ${SNAPSHOT_CLASS_NAME}
  source:
    persistentVolumeClaimName: ${PVC_NAME}
EOF
    
    # Wait for snapshot to be ready
    echo ""
    test_info "Waiting for snapshot to be ready (timeout: ${TIMEOUT_PVC})..."
    
    local retries=0
    local max_retries=60
    while [[ $retries -lt $max_retries ]]; do
        local ready_to_use
        ready_to_use=$(kubectl get volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
        
        if [[ "${ready_to_use}" == "true" ]]; then
            test_success "Snapshot is ready"
            break
        fi
        
        sleep 2
        retries=$((retries + 1))
    done
    
    if [[ $retries -eq $max_retries ]]; then
        test_error "Timeout waiting for snapshot to be ready"
        kubectl describe volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" || true
        return 1
    fi
    
    echo ""
    echo "=== VolumeSnapshot Details ==="
    kubectl get volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" -o yaml
}

#######################################
# Create detached clone from snapshot
#######################################
create_detached_clone() {
    test_step "Creating detached clone from snapshot"
    
    # Create PVC from snapshot
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_CLONE_NAME}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
  dataSource:
    name: ${SNAPSHOT_NAME}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF
    
    test_info "PVC from snapshot created, waiting for provisioning..."
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for clone PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${PVC_CLONE_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"
    
    test_success "Clone PVC is bound"
    
    # Show controller logs to verify detached clone was used
    echo ""
    echo "=== Controller Logs (clone creation) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=50 | grep -E "detached|send|receive|clone|CreateVolume" || true
    
    echo ""
    echo "=== Clone PVC Details ==="
    kubectl get pvc "${PVC_CLONE_NAME}" -n "${TEST_NAMESPACE}" -o yaml
}

#######################################
# Mount clone and verify data
#######################################
verify_clone_data() {
    local expected_content=$1
    
    test_step "Mounting clone and verifying data"
    
    # Create pod to mount cloned volume
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_CLONE_NAME}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "3600"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_CLONE_NAME}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for clone pod to be ready (timeout: ${TIMEOUT_POD})..."
    kubectl wait --for=condition=Ready pod/"${POD_CLONE_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"
    
    test_success "Clone pod is ready"
    
    # Verify data
    echo ""
    test_info "Verifying cloned data..."
    local content
    content=$(kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1)
    
    if [[ "${content}" == "${expected_content}" ]]; then
        test_success "Clone data verified: ${content}"
    else
        test_error "Data mismatch: expected '${expected_content}', got '${content}'"
        return 1
    fi
    
    # List all files
    echo ""
    echo "=== Files in clone volume ==="
    kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/
}

#######################################
# Delete parent volume and verify clone survives
#######################################
test_clone_independence() {
    test_step "Testing clone independence (deleting parent volume)"
    
    # First, delete the parent pod
    test_info "Deleting parent pod..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "Parent pod deleted"
    
    # Delete the parent PVC
    test_info "Deleting parent PVC..."
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "Parent PVC deleted"
    
    # Wait for PV to be deleted
    test_info "Waiting for parent PV to be cleaned up..."
    sleep 10
    
    # Show controller logs for deletion
    echo ""
    echo "=== Controller Logs (parent deletion) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=30 | grep -E "DeleteVolume|delete" || true
}

#######################################
# Verify clone still works after parent deletion
#######################################
verify_clone_after_parent_deletion() {
    test_step "Verifying clone works after parent deletion"
    
    # Read data from clone
    test_info "Reading data from clone after parent deletion..."
    local content
    content=$(kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1)
    
    if [[ "${content}" == "Detached Clone Test Data" ]]; then
        test_success "Clone data still accessible after parent deletion: ${content}"
    else
        test_error "Clone data inaccessible or corrupted: ${content}"
        return 1
    fi
    
    # Write new data to clone
    test_info "Writing new data to clone..."
    kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Data written after parent deletion' > /data/post-delete.txt"
    
    local new_content
    new_content=$(kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/post-delete.txt)
    
    if [[ "${new_content}" == "Data written after parent deletion" ]]; then
        test_success "Clone is fully writable after parent deletion"
    else
        test_error "Failed to write to clone after parent deletion"
        return 1
    fi
    
    echo ""
    echo "=== Final clone volume contents ==="
    kubectl exec "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/
    
    test_success "Detached clone is fully independent and functional"
}

#######################################
# Cleanup detached clone test resources
#######################################
cleanup_detached_test() {
    test_step "Cleaning up detached clone test resources"
    
    # Delete clone pod
    test_info "Deleting clone pod..."
    kubectl delete pod "${POD_CLONE_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete parent pod (if still exists)
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete clone PVC
    test_info "Deleting clone PVC..."
    kubectl delete pvc "${PVC_CLONE_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete parent PVC (if still exists)
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete snapshot
    test_info "Deleting VolumeSnapshot..."
    kubectl delete volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete VolumeSnapshotClass
    test_info "Deleting VolumeSnapshotClass..."
    kubectl delete volumesnapshotclass "${SNAPSHOT_CLASS_NAME}" --ignore-not-found=true || true
    
    # Delete namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for cleanup
    test_info "Waiting for TrueNAS backend cleanup..."
    sleep 15
    
    test_success "Cleanup complete"
}

#######################################
# Write test data to parent volume
#######################################
write_parent_data() {
    test_step "Writing test data to parent volume"
    
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Detached Clone Test Data' > /data/test.txt"
    
    local content
    content=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
    test_success "Test data written: ${content}"
    
    # Write a larger file too
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of=/data/largefile.bin bs=1M count=10 2>&1 | tail -1
    test_success "Large test file written"
    
    echo ""
    echo "=== Parent volume contents ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/
}

# Run test steps
verify_cluster
deploy_driver "nfs" --set snapshots.enabled=true
wait_for_driver
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${POD_NAME}"
write_parent_data
create_detached_snapshot_class
create_snapshot
create_detached_clone
verify_clone_data "Detached Clone Test Data"
test_clone_independence
verify_clone_after_parent_deletion

# Success
cleanup_detached_test
test_summary "${PROTOCOL}" "PASSED"
