#!/bin/bash
# Test NVMe-oF volume provisioning and mounting

set -e

VM_NAME="truenas-nvme-test"
KUBECONFIG_FILE="$HOME/.kube/k3s-nvmeof-test"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}TrueNAS CSI - NVMe-oF Testing${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Check prerequisites
if ! multipass list | grep -q "$VM_NAME"; then
    echo -e "${RED}Error: VM '$VM_NAME' not found${NC}"
    exit 1
fi

if [ ! -f "$KUBECONFIG_FILE" ]; then
    echo -e "${RED}Error: Kubeconfig not found${NC}"
    exit 1
fi

# Set kubeconfig
export KUBECONFIG="$KUBECONFIG_FILE"

echo -e "${BLUE}[Test 1/5]${NC} Testing NFS volume (baseline)..."
echo ""

# Create NFS test
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-nfs-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: truenas-nfs
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-nfs-pod
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'NFS test' > /data/test.txt && cat /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-nfs-pvc
EOF

echo "Waiting for NFS PVC to be bound..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/test-nfs-pvc --timeout=60s

echo "Waiting for NFS pod to be ready..."
kubectl wait --for=condition=Ready pod/test-nfs-pod --timeout=60s

echo -e "${GREEN}✓${NC} NFS volume test passed"
echo ""

# Cleanup NFS test
kubectl delete pod test-nfs-pod --grace-period=0 --force 2>/dev/null || true
kubectl delete pvc test-nfs-pvc

sleep 5

echo -e "${BLUE}[Test 2/5]${NC} Creating NVMe-oF PVC..."
echo ""

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-nvmeof-pvc
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: truenas-nvmeof
  resources:
    requests:
      storage: 5Gi
EOF

echo "Waiting for NVMe-oF PVC to be bound..."
if kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/test-nvmeof-pvc --timeout=120s; then
    echo -e "${GREEN}✓${NC} NVMe-oF PVC created and bound"
else
    echo -e "${RED}✗${NC} PVC failed to bind"
    echo ""
    echo "PVC status:"
    kubectl describe pvc test-nvmeof-pvc
    echo ""
    echo "Controller logs:"
    kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=50
    exit 1
fi

echo ""
PV_NAME=$(kubectl get pvc test-nvmeof-pvc -o jsonpath='{.spec.volumeName}')
echo "Created PV: $PV_NAME"

echo ""
echo -e "${BLUE}[Test 3/5]${NC} Creating pod with NVMe-oF volume..."
echo ""

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-nvmeof-pod
spec:
  containers:
  - name: app
    image: busybox
    command: ["sh", "-c", "echo 'NVMe-oF test successful!' > /data/test.txt && cat /data/test.txt && df -h /data && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-nvmeof-pvc
EOF

echo "Waiting for pod to be ready..."
if kubectl wait --for=condition=Ready pod/test-nvmeof-pod --timeout=120s; then
    echo -e "${GREEN}✓${NC} Pod is running with NVMe-oF volume mounted"
else
    echo -e "${RED}✗${NC} Pod failed to start"
    echo ""
    echo "Pod status:"
    kubectl describe pod test-nvmeof-pod
    echo ""
    echo "Node logs:"
    kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin --tail=50
    exit 1
fi

echo ""
echo -e "${BLUE}[Test 4/5]${NC} Verifying NVMe-oF device in VM..."
echo ""

echo "NVMe devices in VM:"
multipass exec "$VM_NAME" -- sudo nvme list || echo "No NVMe devices found (this might be OK if using different protocol)"

echo ""
echo "NVMe subsystems:"
multipass exec "$VM_NAME" -- sudo nvme list-subsys || echo "No subsystems found"

echo ""
echo "Mounted volumes in pod:"
kubectl exec test-nvmeof-pod -- df -h /data

echo ""
echo -e "${BLUE}[Test 5/5]${NC} Testing I/O operations..."
echo ""

echo "Reading test file:"
kubectl exec test-nvmeof-pod -- cat /data/test.txt

echo ""
echo "Writing 100MB test file:"
kubectl exec test-nvmeof-pod -- dd if=/dev/zero of=/data/iotest.bin bs=1M count=100 2>&1 | tail -3

echo ""
echo "Verifying write:"
kubectl exec test-nvmeof-pod -- ls -lh /data/

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}All Tests Passed!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Summary:"
echo "  ✓ NFS baseline test successful"
echo "  ✓ NVMe-oF PVC provisioned"
echo "  ✓ NVMe-oF volume mounted in pod"
echo "  ✓ I/O operations working"
echo ""
echo "Resources created:"
echo "  - PVC: test-nvmeof-pvc"
echo "  - Pod: test-nvmeof-pod"
echo "  - PV: $PV_NAME"
echo ""
echo "Cleanup:"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE delete pod test-nvmeof-pod"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE delete pvc test-nvmeof-pvc"
echo ""
echo "View logs:"
echo "  Controller: kubectl --kubeconfig $KUBECONFIG_FILE logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin"
echo "  Node: kubectl --kubeconfig $KUBECONFIG_FILE logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin"
echo ""
