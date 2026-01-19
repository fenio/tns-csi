#!/bin/bash
# Basic CSI Driver Test for Distro Compatibility
# Tests minimal functionality to verify driver works on different K8s distributions
# This is a lightweight test designed to run quickly across multiple distros

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

# Determine protocol from environment variable or default to NFS
TEST_PROTOCOL="${TEST_PROTOCOL:-nfs}"
PROTOCOL_UPPER=$(echo "${TEST_PROTOCOL}" | tr '[:lower:]' '[:upper:]')

# Set protocol-specific variables
case "${TEST_PROTOCOL}" in
    nfs)
        PVC_NAME="test-pvc-nfs"
        POD_NAME="test-pod-nfs"
        MANIFEST_PVC="${SCRIPT_DIR}/manifests/pvc-nfs.yaml"
        MANIFEST_POD="${SCRIPT_DIR}/manifests/pod-nfs.yaml"
        WAIT_FOR_BINDING="true"
        ;;
    nvmeof)
        PVC_NAME="test-pvc-nvmeof"
        POD_NAME="test-pod-nvmeof"
        MANIFEST_PVC="${SCRIPT_DIR}/manifests/pvc-nvmeof.yaml"
        MANIFEST_POD="${SCRIPT_DIR}/manifests/pod-nvmeof.yaml"
        WAIT_FOR_BINDING="false"  # NVMe-oF uses WaitForFirstConsumer
        ;;
    iscsi)
        PVC_NAME="test-pvc-iscsi"
        POD_NAME="test-pod-iscsi"
        MANIFEST_PVC="${SCRIPT_DIR}/manifests/pvc-iscsi.yaml"
        MANIFEST_POD="${SCRIPT_DIR}/manifests/pod-iscsi.yaml"
        WAIT_FOR_BINDING="false"  # iSCSI uses WaitForFirstConsumer
        ;;
    *)
        echo "Error: Unknown protocol '${TEST_PROTOCOL}'. Must be 'nfs', 'nvmeof', or 'iscsi'"
        exit 1
        ;;
esac

# Get distro name from kubeconfig or cluster info
DISTRO_NAME=$(kubectl version --short 2>/dev/null | grep -i 'Server' | awk '{print $2}' || echo "Unknown")
if [[ "${DISTRO_NAME}" == "Unknown" ]]; then
    # Try to detect from node labels or other means
    if kubectl get nodes -o yaml | grep -q "k3s.io"; then
        DISTRO_NAME="K3s"
    elif kubectl get nodes -o yaml | grep -q "k0s.io"; then
        DISTRO_NAME="K0s"
    elif kubectl get nodes -o yaml | grep -q "minikube"; then
        DISTRO_NAME="Minikube"
    else
        DISTRO_NAME="Generic Kubernetes"
    fi
fi

echo "========================================"
echo "TrueNAS CSI - Distro Compatibility Test"
echo "Distribution: ${DISTRO_NAME}"
echo "Protocol: ${PROTOCOL_UPPER}"
echo "========================================"

# Configure test with 6 steps:
# verify_cluster, deploy_driver, wait_for_driver, create_pvc, create_test_pod, test_io_operations
set_test_steps 6

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${DISTRO_NAME} - ${PROTOCOL_UPPER}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "${TEST_PROTOCOL}"
wait_for_driver

# For NVMe-oF, check if it's configured before proceeding
if [[ "${TEST_PROTOCOL}" == "nvmeof" ]]; then
    if ! check_nvmeof_configured "${MANIFEST_PVC}" "${PVC_NAME}" "${PROTOCOL_UPPER}"; then
        exit 0  # Gracefully skip test if not configured
    fi
fi

# For iSCSI, check if it's configured before proceeding
if [[ "${TEST_PROTOCOL}" == "iscsi" ]]; then
    if ! check_iscsi_configured "${MANIFEST_PVC}" "${PVC_NAME}" "${PROTOCOL_UPPER}"; then
        exit 0  # Gracefully skip test if not configured
    fi
fi

create_pvc "${MANIFEST_PVC}" "${PVC_NAME}" "${WAIT_FOR_BINDING}"
create_test_pod "${MANIFEST_POD}" "${POD_NAME}"
test_io_operations "${POD_NAME}" "/data" "filesystem"
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${DISTRO_NAME} - ${PROTOCOL_UPPER}" "PASSED"
