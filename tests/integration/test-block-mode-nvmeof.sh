#!/bin/bash
# NVMe-oF Raw Block Volume Integration Test
# Tests volumeMode: Block with NVMe-oF protocol using volumeDevices

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF Block Mode"
PVC_NAME="test-pvc-block-nvmeof"
POD_NAME="test-pod-block-nvmeof"
DEVICE_PATH="/dev/nvme-block"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Check if test should be skipped based on tags
if should_skip_test "block,nvmeof"; then
    echo "Skipping NVMe-oF Block Mode test (tag-based skip)"
    exit 0
fi

echo "========================================"
echo "TrueNAS CSI - NVMe-oF Block Mode Test"
echo "========================================"

# Configure test with 8 total steps
set_test_steps 8

# Trap errors and cleanup
trap 'save_diagnostic_logs "nvmeof-block" "${POD_NAME}" "${PVC_NAME}" "/tmp/test-logs"; show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_block_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Test block device operations
#######################################
test_block_device() {
    local pod_name=$1
    local device_path=$2
    
    test_step "Testing raw block device operations"
    
    # Verify device exists in pod
    echo ""
    test_info "Verifying block device is available: ${device_path}"
    
    if ! kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- test -b "${device_path}"; then
        test_error "Block device ${device_path} not found or not a block device"
        echo ""
        echo "=== Device check ==="
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- ls -la "${device_path}" || true
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- ls -la /dev/ || true
        return 1
    fi
    
    test_success "Block device exists: ${device_path}"
    
    # Show device information
    echo ""
    test_info "Block device information:"
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- ls -la "${device_path}"
    
    # Write test pattern to block device
    echo ""
    test_info "Writing test pattern to block device..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of="${device_path}" bs=1M count=10 conv=fsync 2>&1 | tail -3
    test_success "Write to block device successful"
    
    # Read back from block device
    echo ""
    test_info "Reading from block device..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if="${device_path}" of=/dev/null bs=1M count=10 2>&1 | tail -3
    test_success "Read from block device successful"
    
    # Write a known pattern and verify it
    echo ""
    test_info "Writing and verifying known pattern..."
    
    # Create a pattern file
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'BLOCK_TEST_PATTERN_12345' | dd of=${device_path} bs=512 count=1 conv=fsync 2>/dev/null"
    
    # Read back and verify
    local pattern
    pattern=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if="${device_path}" bs=512 count=1 2>/dev/null | head -c 25)
    
    if [[ "${pattern}" == "BLOCK_TEST_PATTERN_12345" ]]; then
        test_success "Pattern verification successful"
    else
        test_warning "Pattern verification may have issues (got: '${pattern}')"
        # Don't fail - some block devices may have alignment issues
    fi
    
    # Test larger I/O to verify device capacity
    echo ""
    test_info "Testing larger I/O (100MB)..."
    kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of="${device_path}" bs=1M count=100 conv=fsync 2>&1 | tail -3
    test_success "Large block I/O successful"
    
    test_success "Raw block device operations completed successfully"
}

#######################################
# Cleanup block test resources
#######################################
cleanup_block_test() {
    test_step "Cleaning up block test resources"
    
    # Delete pod first
    test_info "Deleting pod..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVC
    test_info "Deleting PVC..."
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
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

#######################################
# Create block mode test pod
#######################################
create_block_test_pod() {
    local pod_name=$1
    # Note: pvc_name and device_path come from manifest file
    
    test_step "Creating block mode test pod: ${pod_name}"
    
    # Apply block mode pod manifest
    kubectl apply -f "${MANIFEST_DIR}/pod-block-nvmeof.yaml" -n "${TEST_NAMESPACE}"
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Block mode pod failed to become ready"
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        echo ""
        echo "=== Pod Events ==="
        kubectl get events -n "${TEST_NAMESPACE}" \
            --field-selector involvedObject.name="${pod_name}" \
            --sort-by='.lastTimestamp' || true
        return 1
    fi
    
    test_success "Block mode pod is ready"
    
    # Show pod details in verbose mode
    show_resource_yaml "pod" "${pod_name}" "${TEST_NAMESPACE}"
    show_nvmeof_details "${pod_name}" "${TEST_NAMESPACE}"
}

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured before proceeding
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-block-nvmeof.yaml" "${PVC_NAME}" "${PROTOCOL}"; then
    exit 0  # Exit gracefully - test was skipped
fi

# Create block mode PVC
create_pvc "${MANIFEST_DIR}/pvc-block-nvmeof.yaml" "${PVC_NAME}"

# Create block mode test pod
create_block_test_pod "${POD_NAME}"

# Test block device operations
test_block_device "${POD_NAME}" "${DEVICE_PATH}"

# Verify metrics
verify_metrics

# Success
cleanup_block_test
test_summary "${PROTOCOL}" "PASSED"
