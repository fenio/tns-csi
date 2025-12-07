#!/bin/bash
# NFS Integration Test
# Tests NFS volume provisioning, mounting, and I/O operations

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS"
PVC_NAME="test-pvc-nfs"
POD_NAME="test-pod-nfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
TEST_TAGS="basic,expansion,metrics,nfs"

echo "========================================"
echo "TrueNAS CSI - NFS Integration Test"
echo "========================================"

# Configure test with 7 total steps:
# verify_cluster, deploy_driver, wait_for_driver, create_pvc,
# create_test_pod, test_io_operations, test_volume_expansion
set_test_steps 7

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping NFS test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver
create_pvc "${MANIFEST_DIR}/pvc-nfs.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nfs.yaml" "${POD_NAME}"
test_io_operations "${POD_NAME}" "/data" "filesystem"
test_volume_expansion "${PVC_NAME}" "${POD_NAME}" "/data" "3Gi"
verify_metrics
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
