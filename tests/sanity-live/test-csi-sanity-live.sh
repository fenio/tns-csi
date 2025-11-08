#!/bin/bash

# CSI Sanity Live Test Runner
# Runs csi-sanity binary against a live k3s cluster with deployed tns-csi driver

set -e
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Protocol to test (nfs or nvmeof)
PROTOCOL="${1:-nfs}"

echo "=== CSI Sanity Live Tests (${PROTOCOL}) ==="
echo "Project root: ${PROJECT_ROOT}"

# Verify kubectl is working
if ! kubectl get nodes &>/dev/null; then
    echo "❌ kubectl is not configured or cluster is not accessible"
    exit 1
fi

# Namespace where CSI driver is deployed (default: kube-system)
CSI_NAMESPACE="${CSI_NAMESPACE:-kube-system}"

# Verify CSI driver is deployed
if ! kubectl get pods -n "${CSI_NAMESPACE}" -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller | grep -q "tns-csi"; then
    echo "❌ CSI driver controller not found in ${CSI_NAMESPACE} namespace"
    exit 1
fi

if ! kubectl get pods -n "${CSI_NAMESPACE}" -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node | grep -q "tns-csi"; then
    echo "❌ CSI driver node not found in ${CSI_NAMESPACE} namespace"
    exit 1
fi

echo "✅ CSI driver is deployed"

# Get the CSI socket path from the node DaemonSet
# The CSI socket is mounted at /csi in the container, which maps to /var/lib/kubelet/plugins/tns.csi.io/csi.sock on the host
CSI_SOCKET="/var/lib/kubelet/plugins/tns.csi.io/csi.sock"

echo "CSI socket path: ${CSI_SOCKET}"

# Verify the socket exists (needs sudo to access kubelet directory)
if ! sudo test -S "${CSI_SOCKET}"; then
    echo "❌ CSI socket not found at ${CSI_SOCKET}"
    echo "Checking kubelet plugins directory:"
    sudo ls -la /var/lib/kubelet/plugins/ || true
    sudo ls -la /var/lib/kubelet/plugins/tns.csi.io/ || true
    exit 1
fi

echo "✅ CSI socket found"

# Define staging and target directories for csi-sanity
STAGING_DIR="/tmp/csi-sanity-staging-${PROTOCOL}"
TARGET_DIR="/tmp/csi-sanity-target-${PROTOCOL}"

# Clean up any previous test directories (csi-sanity will create them)
sudo rm -rf "${STAGING_DIR}" "${TARGET_DIR}" || true

echo "Staging directory: ${STAGING_DIR}"
echo "Target directory: ${TARGET_DIR}"

# Run csi-sanity
echo ""
echo "=== Running csi-sanity ==="
echo ""

# Path to csi-sanity binary
CSI_SANITY="${HOME}/go/bin/csi-sanity"

if [ ! -x "${CSI_SANITY}" ]; then
    echo "❌ csi-sanity binary not found at ${CSI_SANITY}"
    echo "Please install it with: go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest"
    exit 1
fi

# Run csi-sanity with appropriate flags
# Note: Some tests may be skipped based on driver capabilities
# Run with sudo to access the CSI socket in kubelet directory
sudo -E "${CSI_SANITY}" \
    --csi.endpoint="unix://${CSI_SOCKET}" \
    --csi.stagingdir="${STAGING_DIR}" \
    --csi.mountdir="${TARGET_DIR}" \
    --ginkgo.v \
    --ginkgo.progress \
    --ginkgo.failFast=false

EXIT_CODE=$?

# Clean up test directories
echo ""
echo "=== Cleaning up test directories ==="
sudo rm -rf "${STAGING_DIR}" "${TARGET_DIR}" || true

if [ ${EXIT_CODE} -eq 0 ]; then
    echo ""
    echo "✅ CSI Sanity Live Tests (${PROTOCOL}) PASSED"
    exit 0
else
    echo ""
    echo "❌ CSI Sanity Live Tests (${PROTOCOL}) FAILED"
    echo ""
    echo "Collecting driver logs..."
    echo ""
    echo "=== Controller logs ==="
    kubectl logs -n "${CSI_NAMESPACE}" -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller --tail=50 || true
    echo ""
    echo "=== Node logs ==="
    kubectl logs -n "${CSI_NAMESPACE}" -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node --tail=50 || true
    exit ${EXIT_CODE}
fi
