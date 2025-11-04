#!/bin/bash
# NVMe-oF Integration Test (Filesystem Mode)
# Tests NVMe-oF volume provisioning, mounting, and I/O operations

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF"
PVC_NAME="test-pvc-nvmeof"
POD_NAME="test-pod-nvmeof"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF Integration Test"
echo "========================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured on TrueNAS
test_info "Checking if NVMe-oF is configured on TrueNAS..."

# Create a pre-check PVC to see if provisioning works
kubectl apply -f "${MANIFEST_DIR}/pvc-nvmeof.yaml" || true
sleep 10

# Check controller logs for port configuration error
LOGS=$(kubectl logs -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    --tail=20 2>/dev/null || true)

if echo "$LOGS" | grep -q "No TCP NVMe-oF port"; then
    test_warning "NVMe-oF ports not configured on TrueNAS server"
    test_warning "Skipping NVMe-oF tests - this is expected if NVMe-oF is not set up"
    test_info "To enable NVMe-oF: Configure an NVMe-oF TCP portal in TrueNAS UI"
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --wait=false
    test_summary "${PROTOCOL}" "SKIPPED"
    exit 0
fi

test_success "NVMe-oF is configured, proceeding with tests"

# Continue with full test
create_pvc "${MANIFEST_DIR}/pvc-nvmeof.yaml" "${PVC_NAME}"
create_test_pod "${MANIFEST_DIR}/pod-nvmeof.yaml" "${POD_NAME}"
test_io_operations "${POD_NAME}" "/data" "filesystem"
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
