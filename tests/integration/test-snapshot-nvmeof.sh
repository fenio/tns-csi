#!/bin/bash
# NVMe-oF Snapshot Integration Test
# Tests NVMe-oF volume provisioning, snapshot creation, and restoration

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF Snapshot"
PVC_NAME="test-pvc-nvmeof"
POD_NAME="test-pod-nvmeof"
SNAPSHOT_NAME="test-snapshot-nvmeof"
SNAPSHOT_CLASS_NAME="tns-csi-snapshot-nvmeof"
PVC_FROM_SNAPSHOT="test-pvc-from-snapshot-nvmeof"
POD_FROM_SNAPSHOT="test-pod-from-snapshot-nvmeof"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF Snapshot Test"
echo "========================================"

# Trap errors and cleanup
trap 'save_diagnostic_logs "nvmeof-snapshot" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_snapshot_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create VolumeSnapshot from PVC
#######################################
create_snapshot() {
    local snapshot_manifest=$1
    local snapshot_name=$2
    
    test_step "Creating VolumeSnapshot: ${snapshot_name}"
    
    # Apply VolumeSnapshotClass first
    kubectl apply -f "${MANIFEST_DIR}/volumesnapshotclass-nvmeof.yaml"
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
# Test filesystem I/O with data pattern
#######################################
test_block_io_with_pattern() {
    local pod_name=$1
    local mount_path=$2
    
    test_step "Testing filesystem I/O with data pattern"
    
    # Write pattern to filesystem
    echo ""
    test_info "Writing test pattern to filesystem..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'NVMeOF-CSI-TEST-PATTERN' > ${mount_path}/test-pattern.txt"
    test_success "Test pattern written"
    
    # Write additional data file
    echo ""
    test_info "Writing large test data (100MB)..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of="${mount_path}/testfile.dat" bs=1M count=100 2>&1 | tail -3
    test_success "Large data write successful"
    
    # Sync filesystem to ensure data is written to disk
    echo ""
    test_info "Syncing filesystem to disk..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- sync
    test_success "Filesystem synced"
    
    # Verify files exist before snapshot
    echo ""
    test_info "Verifying files exist before snapshot..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- ls -lh "${mount_path}/"
    
    # Verify pattern can be read
    local verify_pattern
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/test-pattern.txt"; then
        test_error "CRITICAL: test-pattern.txt does not exist before snapshot!"
        return 1
    fi
    
    if ! verify_pattern=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test-pattern.txt" 2>/dev/null); then
        test_error "Failed to read test-pattern.txt before snapshot!"
        test_error "Error: ${verify_pattern}"
        return 1
    fi
    
    if [[ "${verify_pattern}" == *"NVMeOF-CSI-TEST-PATTERN"* ]]; then
        test_success "Pattern verified before snapshot"
    else
        test_error "Pattern verification failed before snapshot (got: '${verify_pattern}')"
        return 1
    fi
}

#######################################
# Verify filesystem pattern
#######################################
verify_block_pattern() {
    local pod_name=$1
    local mount_path=$2
    
    test_info "Verifying test pattern from snapshot..."
    
    # Read file and verify pattern
    local pattern
    if ! pod_file_exists "${pod_name}" "${TEST_NAMESPACE}" "${mount_path}/test-pattern.txt"; then
        test_error "CRITICAL: test-pattern.txt does not exist in restored snapshot!"
        return 1
    fi
    
    if ! pattern=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/test-pattern.txt" 2>/dev/null); then
        test_error "Failed to read test-pattern.txt from snapshot!"
        test_error "Error: ${pattern}"
        return 1
    fi
    
    if [[ "${pattern}" == *"NVMeOF-CSI-TEST-PATTERN"* ]]; then
        test_success "Filesystem pattern verified"
        return 0
    else
        test_error "Pattern verification failed (got: '${pattern}')"
        return 1
    fi
}

#######################################
# Create PVC from snapshot and test data persistence
#######################################
test_snapshot_restore() {
    local pvc_manifest=$1
    local pvc_name=$2
    local pod_name=$3
    
    test_step "Testing snapshot restore: ${pvc_name}"
    
    # Show PVC manifest for debugging
    echo ""
    echo "=== PVC-from-snapshot Manifest ==="
    cat "${pvc_manifest}"
    echo ""
    
    # Create PVC from snapshot
    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}"
    
    # Note: With WaitForFirstConsumer, PVC won't bind until pod is created
    echo ""
    test_info "PVC created from snapshot (will bind when pod is created)"
    
    # Check PVC status immediately after creation
    echo ""
    echo "=== PVC Status (immediately after creation) ==="
    kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml
    echo ""
    
    # Wait a moment for provisioner to process
    test_info "Waiting 5 seconds for provisioner to process PVC..."
    sleep 5
    
    # Check controller logs for CreateVolume calls
    echo ""
    echo "=== Controller Logs (after PVC creation) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c tns-csi-plugin \
        --tail=50 | grep -A 10 -B 5 "CreateVolume\|VolumeContentSource\|snapshot" || echo "No CreateVolume calls found"
    echo ""
    
    # Check csi-provisioner logs
    echo ""
    echo "=== CSI Provisioner Sidecar Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c csi-provisioner \
        --tail=50 || echo "No provisioner logs found"
    echo ""
    
    # Check PVC events
    echo ""
    echo "=== PVC Events ==="
    kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" | grep -A 20 "Events:" || echo "No events found"
    echo ""
    
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
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${pvc_name}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        echo ""
        echo "=== POD WAIT FAILED - Collecting diagnostics ==="
        echo ""
        echo "--- PVC Status ---"
        kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml || true
        echo ""
        echo "--- PVC Events ---"
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" | grep -A 30 "Events:" || true
        echo ""
        echo "--- Pod Status ---"
        kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" -o yaml || true
        echo ""
        echo "--- Pod Events ---"
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" | grep -A 30 "Events:" || true
        echo ""
        echo "--- Controller Logs (last 100 lines) ---"
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            -c tns-csi-plugin \
            --tail=100 || true
        echo ""
        echo "--- CSI Provisioner Logs (last 50 lines) ---"
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            -c csi-provisioner \
            --tail=50 || true
        echo ""
        echo "--- Node Logs (last 50 lines) ---"
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
            --tail=50 || true
        echo ""
        echo "=== END DIAGNOSTICS ==="
        return 1
    fi
    
    test_success "Pod is ready"
    
    # Verify PVC is now bound
    echo ""
    test_info "Verifying PVC is bound..."
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    if [[ "${pvc_status}" == "Bound" ]]; then
        test_success "PVC from snapshot is bound"
    else
        test_error "PVC is not bound (status: ${pvc_status})"
        return 1
    fi
    
    # Verify data pattern from snapshot
    echo ""
    test_info "Listing cloned volume contents..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- ls -lah /data/ || test_warning "Failed to list /data"
    
    echo ""
    test_info "=== CSI Node Logs (needsFormat check for snapshot restore) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=100 | grep -A 5 -B 5 "needsFormat\|invalidateDeviceCache\|NodeStageVolume.*snap" || true
    echo ""
    
    verify_block_pattern "${pod_name}" "/data"
    
    # Write new data to cloned volume
    echo ""
    test_info "Writing new data to cloned filesystem..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'CLONED-VOLUME-DATA' > /data/cloned-data.txt"
    test_success "Write to cloned volume successful"
    
    # Verify new data
    echo ""
    test_info "Verifying new data on cloned volume..."
    local new_pattern
    new_pattern=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        cat /data/cloned-data.txt 2>/dev/null || echo "")
    
    if [[ "${new_pattern}" == *"CLONED-VOLUME-DATA"* ]]; then
        test_success "New data verified on cloned volume"
    else
        test_error "Failed to verify new data (got: '${new_pattern}')"
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
deploy_driver "nvmeof" --set snapshots.enabled=true --set snapshots.volumeSnapshotClass.create=true
wait_for_driver

# Check if NVMe-oF is configured on TrueNAS
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-nvmeof.yaml" "test-pvc-nvmeof" "${PROTOCOL}"; then
    exit 0  # Gracefully skip test if not configured
fi

create_pvc "${MANIFEST_DIR}/pvc-nvmeof.yaml" "${PVC_NAME}" false  # Don't wait for binding (WaitForFirstConsumer)
create_test_pod "${MANIFEST_DIR}/pod-nvmeof.yaml" "${POD_NAME}"
test_block_io_with_pattern "${POD_NAME}" "/data"
create_snapshot "${MANIFEST_DIR}/volumesnapshot-nvmeof.yaml" "${SNAPSHOT_NAME}"
test_snapshot_restore "${MANIFEST_DIR}/pvc-from-snapshot-nvmeof.yaml" "${PVC_FROM_SNAPSHOT}" "${POD_FROM_SNAPSHOT}"
verify_metrics

# Success
cleanup_snapshot_test
test_summary "${PROTOCOL}" "PASSED"
