#!/bin/bash
# Basic CSI Test - Quick validation of mount and resize functionality
# This test is designed to run quickly across multiple K8s distributions

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

# Get protocol from environment or default to nfs
TEST_PROTOCOL="${TEST_PROTOCOL:-nfs}"
PROTOCOL="Basic CSI (${TEST_PROTOCOL})"
PVC_NAME="test-pvc-${TEST_PROTOCOL}"
POD_NAME="test-pod-${TEST_PROTOCOL}"

echo "================================================"
echo "TrueNAS CSI - Basic Functionality Test"
echo "Protocol: ${TEST_PROTOCOL}"
echo "================================================"
echo ""
echo "This test verifies:"
echo "  • Volume provisioning"
echo "  • Pod mount operations"
echo "  • Basic I/O operations"
echo "  • Volume expansion"
echo "================================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "${TEST_PROTOCOL}"
wait_for_driver

# For NVMe-oF, check if it's configured
if [[ "${TEST_PROTOCOL}" == "nvmeof" ]]; then
    PVC_MANIFEST="${SCRIPT_DIR}/manifests/pvc-nvmeof.yaml"
    POD_MANIFEST="${SCRIPT_DIR}/manifests/pod-nvmeof.yaml"
    
    if ! check_nvmeof_configured "${PVC_MANIFEST}" "${PVC_NAME}" "Basic NVMe-oF"; then
        exit 0
    fi
else
    PVC_MANIFEST="${SCRIPT_DIR}/manifests/pvc-nfs.yaml"
    POD_MANIFEST="${SCRIPT_DIR}/manifests/pod-nfs.yaml"
fi

# Create PVC and wait for binding
create_pvc "${PVC_MANIFEST}" "${PVC_NAME}" "true"

# Create test pod
create_test_pod "${POD_MANIFEST}" "${POD_NAME}"

# Test I/O operations
test_io_operations "${POD_NAME}" "/data" "filesystem"

# Test volume expansion (from 1Gi to 2Gi)
test_volume_expansion "${PVC_NAME}" "${POD_NAME}" "/data" "2Gi"

# Verify metrics (quick check)
verify_metrics

# Cleanup
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
