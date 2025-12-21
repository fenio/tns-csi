#!/bin/bash
# Standalone Test 1: NFS Volume Creation
# No dependencies - everything self-contained

set -e

TEST_NAME="NFS Volume Creation"
NAMESPACE="test-nfs-create-$$"
PVC_NAME="test-pvc"
POD_NAME="test-pod"

echo "========================================"
echo "Test: ${TEST_NAME}"
echo "========================================"

# Cleanup function
cleanup() {
    echo ""
    echo "=== Cleanup ==="
    kubectl delete namespace "${NAMESPACE}" --timeout=120s --ignore-not-found=true || true
    kubectl delete pv -l test-run="${NAMESPACE}" --ignore-not-found=true || true
}

# Trap to ensure cleanup
trap cleanup EXIT

# Step 1: Verify cluster
echo ""
echo "Step 1: Verify cluster access"
if ! kubectl cluster-info &>/dev/null; then
    echo "ERROR: Cannot access cluster"
    exit 1
fi
kubectl get nodes
echo "✓ Cluster is accessible"

# Step 2: Create namespace
echo ""
echo "Step 2: Create test namespace"
kubectl create namespace "${NAMESPACE}"
echo "✓ Namespace created: ${NAMESPACE}"

# Step 3: Build and push CSI driver image
echo ""
echo "Step 3: Build CSI driver"
cd /Users/bfenski/tns-csi
make build
echo "✓ Driver built"

echo ""
echo "Note: Image will be pulled from registry (bfenski/tns-csi:latest)"
echo "Ensure the latest image is pushed to Docker Hub before running this test"

# Step 4: Create TrueNAS secret
echo ""
echo "Step 4: Create TrueNAS credentials secret"
kubectl create secret generic tns-csi-secret \
    --namespace=kube-system \
    --from-literal=url="wss://${TRUENAS_HOST}/api/current" \
    --from-literal=api-key="${TRUENAS_API_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f -
echo "✓ Secret created"

# Step 5: Deploy CSI driver
echo ""
echo "Step 5: Deploy CSI driver"
helm upgrade --install tns-csi ./charts/tns-csi-driver \
    --namespace kube-system \
    --set image.repository=bfenski/tns-csi \
    --set image.tag="${CSI_IMAGE_TAG:-latest}" \
    --set image.pullPolicy=Always \
    --set truenas.existingSecret=tns-csi-secret \
    --set storageClasses.nfs.enabled=true \
    --set storageClasses.nfs.pool="${TRUENAS_POOL}" \
    --set storageClasses.nfs.server="${TRUENAS_HOST}" \
    --set storageClasses.nvmeof.enabled=false \
    --wait --timeout=3m

echo "✓ CSI driver deployed"

# Step 6: Wait for driver pods
echo ""
echo "Step 6: Wait for driver pods to be ready"
kubectl wait --for=condition=Ready pod \
    -l app.kubernetes.io/name=tns-csi-driver \
    -n kube-system \
    --timeout=120s
echo "✓ Driver pods are ready"

kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# Step 7: Create PVC
echo ""
echo "Step 7: Create PVC"
cat <<EOF | kubectl apply -n "${NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

echo "Waiting for PVC to be bound..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/${PVC_NAME} \
    -n "${NAMESPACE}" \
    --timeout=120s

echo "✓ PVC is bound"
kubectl get pvc -n "${NAMESPACE}"

# Step 8: Create test pod
echo ""
echo "Step 8: Create test pod"
cat <<EOF | kubectl apply -n "${NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: test
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "echo 'Ready' && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF

echo "Waiting for pod to be ready..."
kubectl wait --for=condition=Ready pod/${POD_NAME} \
    -n "${NAMESPACE}" \
    --timeout=120s

echo "✓ Pod is ready"
kubectl get pod -n "${NAMESPACE}"

# Step 9: Test I/O
echo ""
echo "Step 9: Test I/O operations"

echo "Writing test file..."
kubectl exec ${POD_NAME} -n "${NAMESPACE}" -- sh -c "echo 'Hello CSI!' > /data/test.txt"
echo "✓ Write successful"

echo "Reading test file..."
CONTENT=$(kubectl exec ${POD_NAME} -n "${NAMESPACE}" -- cat /data/test.txt)
if [[ "${CONTENT}" == "Hello CSI!" ]]; then
    echo "✓ Read successful: ${CONTENT}"
else
    echo "ERROR: Read failed. Expected 'Hello CSI!', got '${CONTENT}'"
    exit 1
fi

echo "Testing df..."
kubectl exec ${POD_NAME} -n "${NAMESPACE}" -- df -h /data

echo ""
echo "========================================"
echo "✓ ${TEST_NAME}: PASSED"
echo "========================================"
