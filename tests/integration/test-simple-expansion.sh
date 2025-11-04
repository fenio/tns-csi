#!/bin/bash
# Simple volume expansion test - minimal reproducer
# This tests the most basic expansion scenario

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Simple Expansion Test"
NAMESPACE="test-expansion-$(date +%s)"
PVC_NAME="simple-pvc"

echo "========================================"
echo "Simple Volume Expansion Test"
echo "========================================"

# Create namespace
echo "Creating namespace: ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}"

# Deploy driver
deploy_driver "nfs"
wait_for_driver

# Create a simple PVC manifest inline
echo ""
echo "Creating PVC with 1Gi size..."
cat <<EOF | kubectl apply -n "${NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

# Wait for PVC to be bound
echo "Waiting for PVC to be bound..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${NAMESPACE}" \
    --timeout=120s

# Show PVC status
echo ""
echo "=== PVC after creation ==="
kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o yaml

echo ""
echo "Current size in spec: $(kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.resources.requests.storage}')"
echo "Current size in status: $(kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.capacity.storage}')"

# Try to expand
echo ""
echo "Attempting to expand PVC to 2Gi..."
kubectl patch pvc "${PVC_NAME}" -n "${NAMESPACE}" \
    -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}'

# Wait and show result
sleep 10

echo ""
echo "=== PVC after expansion attempt ==="
kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o yaml

echo ""
echo "Size in spec after expansion: $(kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.resources.requests.storage}')"
echo "Size in status after expansion: $(kubectl get pvc "${PVC_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.capacity.storage}')"

# Check controller logs
echo ""
echo "=== Controller logs (last 50 lines) ==="
kubectl logs -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    --tail=50 || true

# Cleanup
echo ""
echo "Cleaning up..."
kubectl delete namespace "${NAMESPACE}" --ignore-not-found=true --timeout=60s

echo ""
echo "========================================"
echo "Test Complete"
echo "========================================"
