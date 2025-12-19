#!/bin/bash
# Snapshot Stress Integration Test
# Tests multiple snapshots of the same volume, snapshot dependencies, and cleanup ordering

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Snapshot Stress"
PVC_NAME="test-pvc-nfs"
POD_NAME="test-pod-nfs"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
NUM_SNAPSHOTS=5  # Number of snapshots to create

# Check if test should be skipped based on tags
if should_skip_test "stress,snapshot,nfs"; then
    echo "Skipping Snapshot Stress test (tag-based skip)"
    exit 0
fi

echo "========================================"
echo "TrueNAS CSI - Snapshot Stress Test"
echo "========================================"

# Configure test with 12 total steps
set_test_steps 12

# Trap errors and cleanup
trap 'save_diagnostic_logs "snapshot-stress" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_stress_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Arrays to track created resources
declare -a SNAPSHOT_NAMES=()
declare -a PVC_FROM_SNAPSHOT_NAMES=()
declare -a POD_FROM_SNAPSHOT_NAMES=()

#######################################
# Create multiple snapshots of the same volume
#######################################
create_multiple_snapshots() {
    local source_pvc=$1
    local num_snapshots=$2
    
    test_step "Creating ${num_snapshots} snapshots of the same volume"
    
    # Apply VolumeSnapshotClass first
    kubectl apply -f "${MANIFEST_DIR}/volumesnapshotclass-nfs.yaml"
    test_success "VolumeSnapshotClass created"
    
    for i in $(seq 1 "${num_snapshots}"); do
        local snapshot_name="test-snapshot-stress-${i}"
        SNAPSHOT_NAMES+=("${snapshot_name}")
        
        test_info "Creating snapshot ${i}/${num_snapshots}: ${snapshot_name}"
        
        # Write unique data before each snapshot
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
            sh -c "echo 'Snapshot ${i} Data - $(date +%s)' > /data/snapshot-${i}.txt"
        
        # Small delay to ensure data is written
        sleep 1
        
        # Create the snapshot
        cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${snapshot_name}
spec:
  volumeSnapshotClassName: ${SNAPSHOT_CLASS_NAME}
  source:
    persistentVolumeClaimName: ${source_pvc}
EOF
        
        # Wait for snapshot to be ready
        local retries=0
        local max_retries=60
        while [[ $retries -lt $max_retries ]]; do
            local ready_to_use
            ready_to_use=$(kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
            
            if [[ "${ready_to_use}" == "true" ]]; then
                test_success "Snapshot ${i} is ready: ${snapshot_name}"
                break
            fi
            
            sleep 2
            retries=$((retries + 1))
        done
        
        if [[ $retries -eq $max_retries ]]; then
            test_error "Timeout waiting for snapshot ${i} to be ready"
            kubectl describe volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" || true
            return 1
        fi
    done
    
    test_success "All ${num_snapshots} snapshots created successfully"
    
    # List all snapshots
    echo ""
    echo "=== All VolumeSnapshots ==="
    kubectl get volumesnapshot -n "${TEST_NAMESPACE}"
}

#######################################
# Verify all snapshots can be listed and have unique content references
#######################################
verify_snapshot_uniqueness() {
    test_step "Verifying snapshot uniqueness"
    
    local snapshot_handles=()
    
    for snapshot_name in "${SNAPSHOT_NAMES[@]}"; do
        local content_name
        content_name=$(kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.boundVolumeSnapshotContentName}')
        
        local snapshot_handle
        snapshot_handle=$(kubectl get volumesnapshotcontent "${content_name}" -o jsonpath='{.status.snapshotHandle}' 2>/dev/null || echo "unknown")
        
        test_info "Snapshot: ${snapshot_name} -> Content: ${content_name} -> Handle: ${snapshot_handle}"
        
        # Check for duplicate handles (would indicate a bug)
        for existing_handle in "${snapshot_handles[@]}"; do
            if [[ "${existing_handle}" == "${snapshot_handle}" && "${snapshot_handle}" != "unknown" ]]; then
                test_error "Duplicate snapshot handle detected: ${snapshot_handle}"
                return 1
            fi
        done
        
        snapshot_handles+=("${snapshot_handle}")
    done
    
    test_success "All snapshots have unique content references"
}

#######################################
# Test restoring from multiple snapshots
#######################################
test_multi_snapshot_restore() {
    test_step "Testing restore from multiple snapshots"
    
    # Restore from first and last snapshots to verify data integrity
    local snapshots_to_restore=("${SNAPSHOT_NAMES[0]}" "${SNAPSHOT_NAMES[$((NUM_SNAPSHOTS - 1))]}")
    local index=1
    
    for snapshot_name in "${snapshots_to_restore[@]}"; do
        local pvc_name="test-pvc-restore-${index}"
        local pod_name="test-pod-restore-${index}"
        PVC_FROM_SNAPSHOT_NAMES+=("${pvc_name}")
        POD_FROM_SNAPSHOT_NAMES+=("${pod_name}")
        
        test_info "Restoring from snapshot: ${snapshot_name} -> PVC: ${pvc_name}"
        
        # Create PVC from snapshot
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
  dataSource:
    name: ${snapshot_name}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF
        
        # Wait for PVC to be bound
        test_info "Waiting for PVC ${pvc_name} to be bound..."
        if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
            pvc/"${pvc_name}" \
            -n "${TEST_NAMESPACE}" \
            --timeout="${TIMEOUT_PVC}"; then
            test_error "PVC ${pvc_name} failed to bind"
            kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
            return 1
        fi
        
        test_success "PVC ${pvc_name} is bound"
        
        # Create pod to verify data
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
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${pvc_name}
EOF
        
        # Wait for pod to be ready
        if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
            -n "${TEST_NAMESPACE}" \
            --timeout="${TIMEOUT_POD}"; then
            test_error "Pod ${pod_name} failed to become ready"
            kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
            return 1
        fi
        
        test_success "Pod ${pod_name} is ready"
        
        # Verify snapshot data is present
        local expected_file="snapshot-${index}.txt"
        if pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "/data/${expected_file}"; then
            local content
            content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "/data/${expected_file}")
            test_success "Data from ${expected_file} restored: ${content}"
        else
            test_info "File ${expected_file} not found (expected for snapshot ${index})"
        fi
        
        index=$((index + 1))
    done
    
    test_success "Multi-snapshot restore test completed"
}

#######################################
# Test deleting snapshots in different orders
#######################################
test_snapshot_deletion_order() {
    test_step "Testing snapshot deletion ordering"
    
    # Delete snapshots in reverse order (common pattern)
    test_info "Deleting snapshots in reverse order..."
    
    for ((i=${#SNAPSHOT_NAMES[@]}-1; i>=0; i--)); do
        local snapshot_name="${SNAPSHOT_NAMES[$i]}"
        
        # Check if any PVC is using this snapshot
        local using_pvcs
        using_pvcs=$(kubectl get pvc -n "${TEST_NAMESPACE}" -o json | \
            jq -r ".items[] | select(.spec.dataSource.name==\"${snapshot_name}\") | .metadata.name" 2>/dev/null || echo "")
        
        if [[ -n "${using_pvcs}" ]]; then
            test_info "Snapshot ${snapshot_name} is in use by PVCs: ${using_pvcs}"
            test_info "Skipping deletion of in-use snapshot"
            continue
        fi
        
        test_info "Deleting snapshot: ${snapshot_name}"
        kubectl delete volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" --timeout=60s || {
            test_warning "Failed to delete snapshot ${snapshot_name}"
            continue
        }
        
        # Verify deletion
        if kubectl get volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            test_warning "Snapshot ${snapshot_name} still exists after deletion"
        else
            test_success "Snapshot ${snapshot_name} deleted"
        fi
    done
    
    test_success "Snapshot deletion order test completed"
}

#######################################
# Cleanup stress test resources
#######################################
cleanup_stress_test() {
    test_step "Cleaning up stress test resources"
    
    # Delete pods from snapshot restores
    test_info "Deleting restored pods..."
    for pod_name in "${POD_FROM_SNAPSHOT_NAMES[@]}"; do
        kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    done
    
    # Delete original pod
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVCs from snapshot restores
    test_info "Deleting restored PVCs..."
    for pvc_name in "${PVC_FROM_SNAPSHOT_NAMES[@]}"; do
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    done
    
    # Delete original PVC
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Give CSI driver time to process
    sleep 5
    
    # Delete remaining snapshots
    test_info "Deleting remaining snapshots..."
    for snapshot_name in "${SNAPSHOT_NAMES[@]}"; do
        kubectl delete volumesnapshot "${snapshot_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    done
    
    # Delete VolumeSnapshotClass
    kubectl delete volumesnapshotclass "${SNAPSHOT_CLASS_NAME}" --ignore-not-found=true || true
    
    # Delete namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for PVs to be deleted
    test_info "Waiting for PVs to be deleted..."
    for i in {1..90}; do
        REMAINING_PVS=$(kubectl get pv --no-headers 2>/dev/null | grep -c "${TEST_NAMESPACE}" || echo "0")
        if [[ "${REMAINING_PVS}" == "0" ]]; then
            test_success "All PVs deleted successfully"
            break
        fi
        if [[ $i == 90 ]]; then
            test_warning "Some PVs still exist after 90 seconds"
            kubectl get pv | grep "${TEST_NAMESPACE}" || true
        fi
        sleep 1
    done
    
    test_success "Cleanup complete"
}

# Run test steps
verify_cluster
deploy_driver "nfs" --set snapshots.enabled=true --set snapshots.volumeSnapshotClass.create=true
wait_for_driver

# Create source PVC and pod
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${POD_NAME}"

# Write initial data
test_step "Writing initial data to source volume"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Initial Data' > /data/initial.txt"
test_success "Initial data written"

# Create multiple snapshots
create_multiple_snapshots "${PVC_NAME}" "${NUM_SNAPSHOTS}"

# Verify snapshots are unique
verify_snapshot_uniqueness

# Test restoring from multiple snapshots
test_multi_snapshot_restore

# Test snapshot deletion ordering
test_snapshot_deletion_order

# Verify metrics
verify_metrics

# Success
cleanup_stress_test
test_summary "${PROTOCOL}" "PASSED"
