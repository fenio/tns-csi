#!/bin/bash
# Detached Snapshot Integration Test
# Tests that snapshots created with detachedSnapshots=true are independent
# of the parent volume (uses zfs send/receive instead of native ZFS snapshots)
#
# This test validates that:
# 1. Snapshot can be created as an independent dataset (detached snapshot)
# 2. Original volume can be deleted without affecting the snapshot
# 3. New volume can be restored from detached snapshot after parent deletion
# 4. Restored volume contains the correct data

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Detached Snapshot"
PVC_NAME="test-pvc-detached-snap"
POD_NAME="test-pod-detached-snap"
SNAPSHOT_NAME="test-detached-snapshot"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-detached-snap"
PVC_RESTORED_NAME="test-pvc-from-detached-snap"
POD_RESTORED_NAME="test-pod-from-detached-snap"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Test steps
set_test_steps 13

echo "========================================"
echo "TrueNAS CSI - Detached Snapshot Test"
echo "========================================"
echo ""
echo "This test validates that snapshots created with detachedSnapshots=true"
echo "are stored as independent datasets (via zfs send/receive) and can survive"
echo "the deletion of the source volume."
echo ""

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_detached_snapshot_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create VolumeSnapshotClass with detached snapshots mode
#######################################
create_detached_snapshot_class() {
    test_step "Creating VolumeSnapshotClass with detachedSnapshots=true"
    
    cat <<EOF | kubectl apply -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: ${SNAPSHOT_CLASS_NAME}
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  detachedSnapshots: "true"
  pool: "storage"
  parentDataset: "storage/k8s"
  protocol: "nfs"
EOF
    
    test_success "VolumeSnapshotClass created with detachedSnapshots=true"
    
    echo ""
    echo "=== VolumeSnapshotClass Details ==="
    kubectl get volumesnapshotclass "${SNAPSHOT_CLASS_NAME}" -o yaml
}

#######################################
# Create VolumeSnapshot from PVC
#######################################
create_detached_snapshot() {
    test_step "Creating detached VolumeSnapshot: ${SNAPSHOT_NAME}"
    
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
    test_info "Waiting for detached snapshot to be ready (this may take longer than native snapshots)..."
    test_info "Timeout: ${TIMEOUT_PVC}"
    
    local retries=0
    local max_retries=120  # Longer timeout for zfs send/receive
    while [[ $retries -lt $max_retries ]]; do
        local ready_to_use
        ready_to_use=$(kubectl get volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
        
        if [[ "${ready_to_use}" == "true" ]]; then
            test_success "Detached snapshot is ready"
            break
        fi
        
        # Show progress
        if [[ $((retries % 10)) -eq 0 ]]; then
            test_info "Still waiting... (${retries}s)"
        fi
        
        sleep 2
        retries=$((retries + 2))
    done
    
    if [[ $retries -ge $max_retries ]]; then
        test_error "Timeout waiting for detached snapshot to be ready"
        kubectl describe volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" || true
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 || true
        return 1
    fi
    
    echo ""
    echo "=== VolumeSnapshot Details ==="
    kubectl get volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" -o yaml
    
    # Show controller logs to verify detached snapshot was used
    echo ""
    echo "=== Controller Logs (snapshot creation) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=50 | grep -E "detached|Detached|send|receive|CreateDetachedClone|snapshot" || true
}

#######################################
# Delete source volume and verify snapshot survives
#######################################
test_snapshot_independence() {
    test_step "Testing snapshot independence (deleting source volume)"
    
    # First, delete the source pod
    test_info "Deleting source pod..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "Source pod deleted"
    
    # Delete the source PVC
    test_info "Deleting source PVC..."
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    test_success "Source PVC deleted"
    
    # Wait for PV to be deleted
    test_info "Waiting for source PV to be cleaned up..."
    sleep 15
    
    # Show controller logs for deletion
    echo ""
    echo "=== Controller Logs (source volume deletion) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=30 | grep -E "DeleteVolume|delete|Delete" || true
    
    # Verify snapshot still exists and is ready
    test_info "Verifying detached snapshot still exists..."
    local ready_to_use
    ready_to_use=$(kubectl get volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
    
    if [[ "${ready_to_use}" == "true" ]]; then
        test_success "Detached snapshot still exists and is ready after source volume deletion"
    else
        test_error "Detached snapshot is no longer ready after source volume deletion"
        kubectl describe volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" || true
        return 1
    fi
}

#######################################
# Restore volume from detached snapshot (after source deletion)
#######################################
restore_from_detached_snapshot() {
    test_step "Restoring volume from detached snapshot (source volume is gone)"
    
    # Create PVC from snapshot
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_RESTORED_NAME}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
  dataSource:
    name: ${SNAPSHOT_NAME}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF
    
    test_info "PVC from detached snapshot created, waiting for provisioning..."
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for restored PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${PVC_RESTORED_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"
    
    test_success "Restored PVC is bound"
    
    # Show controller logs
    echo ""
    echo "=== Controller Logs (restore from detached snapshot) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=50 | grep -E "detached|clone|Clone|CreateVolume|snapshot" || true
}

#######################################
# Mount restored volume and verify data
#######################################
verify_restored_data() {
    local expected_content=$1
    
    test_step "Mounting restored volume and verifying data"
    
    # Create pod to mount restored volume
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_RESTORED_NAME}
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
      claimName: ${PVC_RESTORED_NAME}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for restored pod to be ready (timeout: ${TIMEOUT_POD})..."
    kubectl wait --for=condition=Ready pod/"${POD_RESTORED_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"
    
    test_success "Restored pod is ready"
    
    # Verify data
    echo ""
    test_info "Verifying restored data..."
    local content
    content=$(kubectl exec "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1)
    
    if [[ "${content}" == "${expected_content}" ]]; then
        test_success "Restored data verified: ${content}"
    else
        test_error "Data mismatch: expected '${expected_content}', got '${content}'"
        return 1
    fi
    
    # Verify large file is also present
    echo ""
    test_info "Verifying large test file is restored..."
    kubectl exec "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" -- ls -lh /data/largefile.bin
    test_success "Large file restored from detached snapshot"
    
    # Write new data to verify volume is fully functional
    test_info "Writing new data to restored volume..."
    kubectl exec "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'New data after restore' > /data/new-data.txt"
    
    local new_content
    new_content=$(kubectl exec "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/new-data.txt)
    
    if [[ "${new_content}" == "New data after restore" ]]; then
        test_success "Restored volume is fully writable"
    else
        test_error "Failed to write to restored volume"
        return 1
    fi
    
    echo ""
    echo "=== Restored volume contents ==="
    kubectl exec "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/
}

#######################################
# Cleanup detached snapshot test resources
#######################################
cleanup_detached_snapshot_test() {
    test_step "Cleaning up detached snapshot test resources"
    
    # Delete restored pod
    test_info "Deleting restored pod..."
    kubectl delete pod "${POD_RESTORED_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete source pod (if still exists)
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete restored PVC
    test_info "Deleting restored PVC..."
    kubectl delete pvc "${PVC_RESTORED_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete source PVC (if still exists)
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Wait before deleting snapshot
    sleep 5
    
    # Delete snapshot
    test_info "Deleting VolumeSnapshot..."
    kubectl delete volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "VolumeSnapshot deletion timed out"
    }
    
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
# Write test data to source volume
#######################################
write_source_data() {
    test_step "Writing test data to source volume"
    
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Detached Snapshot Test Data' > /data/test.txt"
    
    local content
    content=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
    test_success "Test data written: ${content}"
    
    # Write a larger file too
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of=/data/largefile.bin bs=1M count=10 2>&1 | tail -1
    test_success "Large test file written (10MB)"
    
    echo ""
    echo "=== Source volume contents ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/
}

# Run test steps
verify_cluster
deploy_driver "nfs" --set snapshots.enabled=true
wait_for_driver
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${POD_NAME}"
write_source_data
create_detached_snapshot_class
create_detached_snapshot
test_snapshot_independence
restore_from_detached_snapshot
verify_restored_data "Detached Snapshot Test Data"

# Success
cleanup_detached_snapshot_test
test_summary "${PROTOCOL}" "PASSED"
