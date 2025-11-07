#!/bin/bash
# NVMe-oF Integration Test
# Tests NVMe-oF volume provisioning, mounting, I/O operations, and expansion

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF"
PVC_NAME="test-pvc-nvmeof"
POD_NAME="test-pod-nvmeof"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
TEST_TAGS="basic,expansion,metrics,nvmeof"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF Integration Test"
echo "========================================"

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping NVMe-oF test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster

# Pre-check: Verify NVMe-oF subsystem exists
echo ""
test_info "Verifying NVMe-oF subsystem configuration..."
SUBSYSTEM_NQN="${NVMEOF_SUBSYSTEM_NQN:-nqn.2005-03.org.truenas:csi-test}"
test_info "Expected subsystem NQN: ${SUBSYSTEM_NQN}"
test_warning "IMPORTANT: The NVMe-oF subsystem with NQN '${SUBSYSTEM_NQN}' must be pre-configured"
test_warning "in TrueNAS (Shares > NVMe-oF Subsystems) with at least one TCP port attached."
test_warning "The CSI driver will use this shared subsystem for all test volumes."
echo ""

deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured on TrueNAS
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-nvmeof.yaml" "${PVC_NAME}" "${PROTOCOL}"; then
    exit 0  # Gracefully skip test if not configured
fi

# Continue with full test (NVMe-oF uses WaitForFirstConsumer binding mode)
create_pvc "${MANIFEST_DIR}/pvc-nvmeof.yaml" "${PVC_NAME}" "false"
create_test_pod "${MANIFEST_DIR}/pod-nvmeof.yaml" "${POD_NAME}"
test_io_operations "${POD_NAME}" "/data" "filesystem"
test_volume_expansion "${PVC_NAME}" "${POD_NAME}" "/data" "3Gi"
verify_metrics
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
