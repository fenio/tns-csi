#!/bin/bash
# NFS Volume Clone Integration Test
# Tests PVC-to-PVC cloning for NFS volumes
#
# This test verifies that:
# 1. A source PVC can be created and data written to it
# 2. A clone PVC can be created from the source PVC using dataSource
# 3. The cloned volume contains the same data as the source
# 4. The clone is independent (modifications to clone don't affect source)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Clone"
SOURCE_PVC_NAME="test-pvc-nfs"
SOURCE_POD_NAME="test-pod-nfs"
CLONE_PVC_NAME="test-pvc-clone-nfs"
CLONE_POD_NAME="test-pod-clone-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
MOUNT_PATH="/data"

echo "========================================"
echo "TrueNAS CSI - NFS Clone Test"
echo "========================================"

# Set total test steps for progress tracking
set_test_steps 10

# Trap errors and cleanup
trap 'save_diagnostic_logs "nfs-clone" "${SOURCE_POD_NAME}" "${SOURCE_PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${SOURCE_POD_NAME}" "${SOURCE_PVC_NAME}"; cleanup_test "${SOURCE_POD_NAME}" "${SOURCE_PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Write unique test data to source volume
#######################################
write_source_data() {
    local pod_name=$1
    local mount_path=$2
    
    test_step "Writing test data to source volume"
    
    # Write unique identifier for clone verification
    local unique_id
    unique_id="clone-test-$(date +%s)-${RANDOM}"
    test_info "Writing unique identifier: ${unique_id}"
    
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo '${unique_id}' > ${mount_path}/clone-marker.txt"
    test_success "Unique marker written"
    
    # Write additional test data
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Source volume data for clone test' > ${mount_path}/source-data.txt"
    test_success "Source data written"
    
    # Write a larger file to verify data integrity
    test_info "Writing 50MB test file for integrity verification..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/urandom of="${mount_path}/large-file.bin" bs=1M count=50 2>&1 | tail -3
    
    # Calculate and store checksum
    local checksum
    checksum=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        md5sum "${mount_path}/large-file.bin" | awk '{print $1}')
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo '${checksum}' > ${mount_path}/checksum.txt"
    test_success "Checksum recorded: ${checksum}"
    
    # Export unique_id for later verification
    export SOURCE_UNIQUE_ID="${unique_id}"
    export SOURCE_CHECKSUM="${checksum}"
}

#######################################
# Create clone PVC and verify data
#######################################
create_and_verify_clone() {
    test_step "Creating clone PVC from source"
    
    # Show manifest
    show_yaml_manifest "${MANIFEST_DIR}/pvc-clone-nfs.yaml" "Clone PVC Manifest"
    
    # Apply clone PVC
    kubectl apply -f "${MANIFEST_DIR}/pvc-clone-nfs.yaml" -n "${TEST_NAMESPACE}"
    
    # Wait for clone PVC to be bound
    echo ""
    test_info "Waiting for clone PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${CLONE_PVC_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        test_error "Clone PVC failed to bind"
        
        echo ""
        echo "=== Clone PVC Status ==="
        kubectl describe pvc "${CLONE_PVC_NAME}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 || true
        
        false
    fi
    
    test_success "Clone PVC is bound"
    
    # Get clone PV name
    local clone_pv
    clone_pv=$(kubectl get pvc "${CLONE_PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    test_info "Clone PV created: ${clone_pv}"
    
    # Show PVC details (verbose only)
    show_resource_yaml "pvc" "${CLONE_PVC_NAME}" "${TEST_NAMESPACE}"
}

#######################################
# Create pod to mount clone and verify data
#######################################
mount_and_verify_clone() {
    test_step "Mounting clone and verifying data"
    
    # Create pod manifest for clone
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${CLONE_POD_NAME}
spec:
  containers:
  - name: test-container
    image: public.ecr.aws/docker/library/busybox:latest
    command: ["sleep", "3600"]
    volumeMounts:
    - name: test-volume
      mountPath: ${MOUNT_PATH}
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${CLONE_PVC_NAME}
EOF
    
    # Wait for pod to be ready
    test_info "Waiting for clone pod to be ready (timeout: ${TIMEOUT_POD})..."
    if ! kubectl wait --for=condition=Ready pod/"${CLONE_POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Clone pod failed to become ready"
        
        echo ""
        echo "=== Clone Pod Status ==="
        kubectl describe pod "${CLONE_POD_NAME}" -n "${TEST_NAMESPACE}" || true
        
        false
    fi
    
    test_success "Clone pod is ready"
    
    # Verify unique marker
    echo ""
    test_info "Verifying cloned data..."
    
    local cloned_marker
    if ! cloned_marker=$(safe_kubectl_exec "${CLONE_POD_NAME}" "${TEST_NAMESPACE}" \
        "cat ${MOUNT_PATH}/clone-marker.txt" "" "reading clone marker"); then
        test_error "Failed to read clone marker"
        false
    fi
    
    if [[ "${cloned_marker}" == "${SOURCE_UNIQUE_ID}" ]]; then
        test_success "Clone marker verified: ${cloned_marker}"
    else
        test_error "Clone marker mismatch: expected '${SOURCE_UNIQUE_ID}', got '${cloned_marker}'"
        false
    fi
    
    # Verify source data
    local cloned_source_data
    if ! cloned_source_data=$(safe_kubectl_exec "${CLONE_POD_NAME}" "${TEST_NAMESPACE}" \
        "cat ${MOUNT_PATH}/source-data.txt" "" "reading source data"); then
        test_error "Failed to read source data from clone"
        false
    fi
    
    if [[ "${cloned_source_data}" == "Source volume data for clone test" ]]; then
        test_success "Source data verified in clone"
    else
        test_error "Source data mismatch in clone"
        false
    fi
    
    # Verify large file checksum
    echo ""
    test_info "Verifying large file integrity..."
    
    local cloned_checksum
    cloned_checksum=$(kubectl exec "${CLONE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        md5sum "${MOUNT_PATH}/large-file.bin" | awk '{print $1}')
    
    if [[ "${cloned_checksum}" == "${SOURCE_CHECKSUM}" ]]; then
        test_success "Large file checksum verified: ${cloned_checksum}"
    else
        test_error "Checksum mismatch: expected '${SOURCE_CHECKSUM}', got '${cloned_checksum}'"
        false
    fi
}

#######################################
# Verify clone independence
#######################################
verify_clone_independence() {
    test_step "Verifying clone independence"
    
    # Write new data to clone
    test_info "Writing new data to clone..."
    kubectl exec "${CLONE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Clone-specific data' > ${MOUNT_PATH}/clone-only.txt"
    test_success "Data written to clone"
    
    # Modify existing file in clone
    kubectl exec "${CLONE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Modified in clone' >> ${MOUNT_PATH}/source-data.txt"
    test_success "Modified existing file in clone"
    
    # Verify source is unchanged
    echo ""
    test_info "Verifying source volume is unchanged..."
    
    # Check source doesn't have clone-only file
    if kubectl exec "${SOURCE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        test -f "${MOUNT_PATH}/clone-only.txt" 2>/dev/null; then
        test_error "Clone-only file appeared in source (clone is NOT independent!)"
        false
    fi
    test_success "Clone-only file not in source"
    
    # Check source data is unchanged
    local source_data
    source_data=$(kubectl exec "${SOURCE_POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        cat "${MOUNT_PATH}/source-data.txt")
    
    if [[ "${source_data}" == "Source volume data for clone test" ]]; then
        test_success "Source data unchanged after clone modification"
    else
        test_error "Source data was modified (clone is NOT independent!)"
        test_error "Expected: 'Source volume data for clone test'"
        test_error "Got: '${source_data}'"
        false
    fi
    
    test_success "Clone is fully independent from source"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${SOURCE_PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${SOURCE_POD_NAME}"
write_source_data "${SOURCE_POD_NAME}" "${MOUNT_PATH}"
create_and_verify_clone
mount_and_verify_clone
verify_clone_independence
verify_metrics

# Success
cleanup_test "${SOURCE_POD_NAME}" "${SOURCE_PVC_NAME}"
test_summary "${PROTOCOL}" "PASSED"
