#!/bin/bash
# NFS Snapshot Integration Test
# Tests NFS volume provisioning, snapshot creation, and restoration

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Snapshot"
PVC_NAME="test-pvc-nfs"
POD_NAME="test-pod-nfs"
SNAPSHOT_NAME="test-snapshot-nfs"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-nfs"
PVC_FROM_SNAPSHOT="test-pvc-from-snapshot-nfs"
POD_FROM_SNAPSHOT="test-pod-from-snapshot-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "========================================"
echo "TrueNAS CSI - NFS Snapshot Test"
echo "========================================"

# Trap errors and cleanup
trap 'save_diagnostic_logs "nfs-snapshot" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_snapshot_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

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
    
    echo ""
    echo "=== VolumeSnapshotContent Details ==="
    kubectl get volumesnapshotcontent "${snapshot_content}" -o yaml || true
}

#######################################
# Create PVC from snapshot and test data persistence
#######################################
test_snapshot_restore() {
    local pvc_manifest=$1
    local pvc_name=$2
    local pod_name=$3
    local mount_path=$4
    local expected_content=$5
    
    test_step "Testing snapshot restore: ${pvc_name}"
    
    # Show controller logs BEFORE creating PVC from snapshot
    echo ""
    echo "=== Controller Logs BEFORE PVC-from-snapshot creation ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=50 || true
    
    # Create PVC from snapshot
    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}"
    test_info "PVC from snapshot created, waiting for provisioning..."
    
    # Wait a moment for the provisioner to pick up the PVC
    sleep 5
    
    # Show controller logs DURING PVC provisioning attempt
    echo ""
    echo "=== Controller Logs DURING PVC-from-snapshot provisioning (5s after creation) ==="
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
    
    test_success "PVC from snapshot is bound"
    
    # Create test pod manifest on the fly
    echo ""
    test_info "Creating pod to mount cloned volume: ${pod_name}"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
spec:
  containers:
  - name: test-container
    image: busybox:latest
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
    test_info "Verifying snapshot data is present..."
    local content
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/test.txt"; then
        test_error "CRITICAL: test.txt does not exist in snapshot!"
        return 1
    fi
    
    if ! content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test.txt" 2>/dev/null); then
        test_error "Failed to read test.txt from snapshot!"
        test_error "Error: ${content}"
        return 1
    fi
    
    test_info "Retrieved: '${content}', Expected: '${expected_content}'"
    
    if [[ "${content}" == "${expected_content}" ]]; then
        test_success "Snapshot data verified: ${content}"
    else
        test_error "Data mismatch: expected '${expected_content}', got '${content}'"
        return 1
    fi
    
    # Verify large file is also present
    echo ""
    test_info "Verifying large test file from snapshot..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        ls -lh "${mount_path}/iotest.bin"
    test_success "Large file restored from snapshot"
    
    # Write new data to cloned volume
    echo ""
    test_info "Writing new data to cloned volume..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Data written to cloned volume' > ${mount_path}/cloned-data.txt"
    
    local new_content
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/cloned-data.txt"; then
        test_error "CRITICAL: cloned-data.txt was not created!"
        return 1
    fi
    
    if ! new_content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/cloned-data.txt" 2>/dev/null); then
        test_error "Failed to read cloned-data.txt!"
        test_error "Error: ${new_content}"
        return 1
    fi
    
    test_info "Retrieved: '${new_content}'"
    
    if [[ "${new_content}" == "Data written to cloned volume" ]]; then
        test_success "Write to cloned volume successful"
    else
        test_error "Failed to write to cloned volume (got: '${new_content}')"
        return 1
    fi
}

#######################################
# Cleanup snapshot test resources
#######################################
cleanup_snapshot_test() {
    test_step "Cleaning up snapshot test resources"
    
    # Delete pods first
    test_info "Deleting pods..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Pod ${POD_NAME} deletion timed out or failed"
    }
    kubectl delete pod "${POD_FROM_SNAPSHOT}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Pod ${POD_FROM_SNAPSHOT} deletion timed out or failed"
    }
    
    # Delete PVCs (this should trigger PV deletion)
    test_info "Deleting PVCs..."
    kubectl delete pvc "${PVC_FROM_SNAPSHOT}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "PVC ${PVC_FROM_SNAPSHOT} deletion timed out after 60s - continuing anyway"
    }
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "PVC ${PVC_NAME} deletion timed out after 60s - continuing anyway"
    }
    
    # Give CSI driver time to process DeleteVolume
    test_info "Waiting 10 seconds for CSI DeleteVolume to process..."
    sleep 10
    
    # Delete snapshot (this should trigger snapshot content deletion)
    test_info "Deleting VolumeSnapshot..."
    kubectl delete volumesnapshot "${SNAPSHOT_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "VolumeSnapshot ${SNAPSHOT_NAME} deletion timed out or failed"
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
    
    # Wait for PVs to be deleted (snapshot test creates 2 PVCs -> 2 PVs)
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
test_snapshot_restore "${MANIFEST_DIR}/pvc-from-snapshot-nfs.yaml" "${PVC_FROM_SNAPSHOT}" "${POD_FROM_SNAPSHOT}" "/data" "CSI Test Data"
verify_metrics

# Success
cleanup_snapshot_test
test_summary "${PROTOCOL}" "PASSED"
