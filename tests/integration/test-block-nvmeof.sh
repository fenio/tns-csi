#!/bin/bash
# NVMe-oF Block Volume Integration Test
# Tests raw block volume mode for NVMe-oF volumes
#
# This test verifies that:
# 1. A block-mode PVC can be created with volumeMode: Block
# 2. A pod can mount the volume as a raw block device
# 3. Raw block I/O operations (read/write) work correctly
# 4. Data persists after pod restart

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF Block"
PVC_NAME="test-pvc-block-nvmeof"
POD_NAME="test-pod-block-nvmeof"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
DEVICE_PATH="/dev/xvda"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF Block Volume Test"
echo "========================================"

# Set total test steps for progress tracking
set_test_steps 9

# Trap errors and cleanup
trap 'save_diagnostic_logs "nvmeof-block" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Verify block device is available
#######################################
verify_block_device() {
    local pod_name=$1
    local device_path=$2
    
    test_step "Verifying block device is available"
    
    # Check device exists
    test_info "Checking device ${device_path} exists..."
    if ! kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        test -b "${device_path}" 2>/dev/null; then
        test_error "Block device ${device_path} does not exist"
        
        echo ""
        echo "=== Available devices ==="
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            ls -la /dev/ 2>/dev/null | head -50 || true
        
        false
    fi
    test_success "Block device ${device_path} exists"
    
    # Get device information
    echo ""
    test_info "Device information:"
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        ls -la "${device_path}" || true
    
    # Try to get block device size
    if kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        command -v blockdev >/dev/null 2>&1; then
        local size
        size=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            blockdev --getsize64 "${device_path}" 2>/dev/null || echo "unknown")
        test_info "Device size: ${size} bytes"
    fi
}

#######################################
# Test raw block I/O operations
#######################################
test_block_io() {
    local pod_name=$1
    local device_path=$2
    
    test_step "Testing raw block I/O operations"
    
    # Generate a unique pattern for verification
    local test_pattern
    test_pattern="BLOCK-TEST-$(date +%s)-${RANDOM}"
    local pattern_length=${#test_pattern}
    
    test_info "Writing test pattern to block device..."
    test_info "Pattern: ${test_pattern} (${pattern_length} bytes)"
    
    # Write pattern to beginning of device
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo -n '${test_pattern}' | dd of=${device_path} bs=\"${pattern_length}\" count=1 conv=notrunc 2>&1" | tail -3
    test_success "Pattern written to block device"
    
    # Sync to ensure data is flushed
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- sync
    
    # Read back and verify
    echo ""
    test_info "Reading pattern back from block device..."
    local read_pattern
    read_pattern=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if="${device_path}" bs="${pattern_length}" count=1 2>/dev/null | tr -d '\0')
    
    if [[ "${read_pattern}" == "${test_pattern}" ]]; then
        test_success "Pattern verified: ${read_pattern}"
    else
        test_error "Pattern mismatch: expected '${test_pattern}', got '${read_pattern}'"
        false
    fi
    
    # Export for persistence test
    export BLOCK_TEST_PATTERN="${test_pattern}"
    export BLOCK_PATTERN_LENGTH="${pattern_length}"
}

#######################################
# Test larger block I/O
#######################################
test_large_block_io() {
    local pod_name=$1
    local device_path=$2
    
    test_step "Testing large block I/O"
    
    # Write 100MB of zeros
    test_info "Writing 100MB to block device..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of="${device_path}" bs=1M count=100 conv=notrunc 2>&1 | tail -3
    test_success "100MB write completed"
    
    # Read back 100MB
    test_info "Reading 100MB from block device..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if="${device_path}" of=/dev/null bs=1M count=100 2>&1 | tail -3
    test_success "100MB read completed"
    
    # Write random data and calculate checksum
    test_info "Writing 10MB random data for checksum test..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/urandom of="${device_path}" bs=1M count=10 conv=notrunc 2>&1 | tail -3
    
    # Read and checksum
    local checksum
    checksum=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "dd if=${device_path} bs=1M count=10 2>/dev/null | md5sum | awk '{print \$1}'")
    test_success "Checksum of random data: ${checksum}"
    
    # Re-read and verify checksum
    local verify_checksum
    verify_checksum=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "dd if=${device_path} bs=1M count=10 2>/dev/null | md5sum | awk '{print \$1}'")
    
    if [[ "${checksum}" == "${verify_checksum}" ]]; then
        test_success "Checksum verified on re-read"
    else
        test_error "Checksum mismatch on re-read: ${checksum} vs ${verify_checksum}"
        false
    fi
}

#######################################
# Test block device persistence
#######################################
test_block_persistence() {
    local device_path=$1
    
    test_step "Testing block device data persistence"
    
    # Write unique marker to device
    local persist_marker
    persist_marker="PERSIST-$(date +%s)-${RANDOM}"
    local marker_length=${#persist_marker}
    
    test_info "Writing persistence marker: ${persist_marker}"
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo -n '${persist_marker}' | dd of=${device_path} bs=\"${marker_length}\" count=1 conv=notrunc 2>&1" | tail -3
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sync
    test_success "Persistence marker written"
    
    # Delete pod
    echo ""
    test_info "Deleting pod to test persistence..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s
    
    # Wait for pod to be deleted
    if ! wait_for_resource_deleted "pod" "${POD_NAME}" "${TEST_NAMESPACE}" 60; then
        test_warning "Pod deletion took longer than expected"
    fi
    test_success "Pod deleted"
    
    # Recreate pod
    echo ""
    test_info "Recreating pod..."
    kubectl apply -f "${MANIFEST_DIR}/pod-block-nvmeof.yaml" -n "${TEST_NAMESPACE}"
    
    # Wait for pod to be ready
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    if ! kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Pod failed to become ready after recreation"
        
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${POD_NAME}" -n "${TEST_NAMESPACE}" || true
        
        false
    fi
    test_success "Pod is ready after recreation"
    
    # Verify persistence marker
    echo ""
    test_info "Verifying persistence marker..."
    local read_marker
    read_marker=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        dd if="${device_path}" bs="${marker_length}" count=1 2>/dev/null | tr -d '\0')
    
    if [[ "${read_marker}" == "${persist_marker}" ]]; then
        test_success "Persistence marker verified: ${read_marker}"
    else
        test_error "Persistence marker mismatch: expected '${persist_marker}', got '${read_marker}'"
        false
    fi
    
    test_success "Block device data persists across pod restarts"
}

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured before proceeding
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-block-nvmeof.yaml" "${PVC_NAME}" "${PROTOCOL}"; then
    exit 0  # Graceful skip - NVMe-oF not configured
fi

# Create block mode PVC (don't wait for binding - WaitForFirstConsumer)
test_step "Creating block-mode PersistentVolumeClaim"
show_yaml_manifest "${MANIFEST_DIR}/pvc-block-nvmeof.yaml" "Block PVC Manifest"
kubectl apply -f "${MANIFEST_DIR}/pvc-block-nvmeof.yaml" -n "${TEST_NAMESPACE}"
test_success "Block PVC created"

# Create test pod with block device
create_test_pod "${MANIFEST_DIR}/pod-block-nvmeof.yaml" "${POD_NAME}"

# Wait for PVC to be bound (should happen after pod is scheduled)
test_info "Waiting for PVC to be bound..."
if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"; then
    test_error "Block PVC failed to bind"
    echo ""
    echo "=== PVC Status ==="
    kubectl describe pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" || true
    false
fi
test_success "Block PVC is bound"

# Run tests
verify_block_device "${POD_NAME}" "${DEVICE_PATH}"
test_block_io "${POD_NAME}" "${DEVICE_PATH}"
test_large_block_io "${POD_NAME}" "${DEVICE_PATH}"
test_block_persistence "${DEVICE_PATH}"
verify_metrics

# Success
cleanup_test "${POD_NAME}" "${PVC_NAME}"
test_summary "${PROTOCOL}" "PASSED"
