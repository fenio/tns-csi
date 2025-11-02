#!/bin/bash
# Deploy TrueNAS CSI driver to the NVMe-oF test VM

set -e

VM_NAME="truenas-nvme-test"
KUBECONFIG_FILE="$HOME/.kube/k3s-nvmeof-test"
IMAGE_NAME="bfenski/tns-csi"
IMAGE_TAG="nvmeof-test"
CREDENTIALS_FILE=".tns-credentials"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Deploy TrueNAS CSI Driver (NVMe-oF)${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Check if VM exists
if ! multipass list | grep -q "$VM_NAME"; then
    echo -e "${RED}Error: VM '$VM_NAME' not found${NC}"
    echo "Run ./scripts/setup-nvmeof-test-vm.sh first"
    exit 1
fi

# Check if kubeconfig exists
if [ ! -f "$KUBECONFIG_FILE" ]; then
    echo -e "${RED}Error: Kubeconfig not found at $KUBECONFIG_FILE${NC}"
    echo "Run ./scripts/setup-nvmeof-test-vm.sh first"
    exit 1
fi

# Load TrueNAS credentials
if [ -f "$CREDENTIALS_FILE" ]; then
    echo -e "${GREEN}[1/6]${NC} Loading TrueNAS credentials..."
    source "$CREDENTIALS_FILE"
else
    echo -e "${RED}Error: $CREDENTIALS_FILE not found${NC}"
    exit 1
fi

# Validate credentials
if [ -z "$TRUENAS_URL" ] || [ -z "$TRUENAS_API_KEY" ]; then
    echo -e "${RED}Error: TRUENAS_URL or TRUENAS_API_KEY not set in $CREDENTIALS_FILE${NC}"
    exit 1
fi

# Extract TrueNAS IP from URL
TRUENAS_IP=$(echo "$TRUENAS_URL" | sed -E 's|wss?://([^:/]+).*|\1|')
echo "TrueNAS IP: $TRUENAS_IP"

echo ""
echo -e "${GREEN}[2/6]${NC} Building Docker image..."
docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" .

echo ""
echo -e "${GREEN}[3/6]${NC} Transferring image to VM..."
IMAGE_TAR="/tmp/tns-csi-${IMAGE_TAG}.tar"
docker save "${IMAGE_NAME}:${IMAGE_TAG}" > "$IMAGE_TAR"
multipass transfer "$IMAGE_TAR" "${VM_NAME}:/tmp/tns-csi.tar"
rm "$IMAGE_TAR"

echo ""
echo -e "${GREEN}[4/6]${NC} Loading image in k3s..."
multipass exec "$VM_NAME" -- sudo k3s ctr images import /tmp/tns-csi.tar
multipass exec "$VM_NAME" -- rm /tmp/tns-csi.tar

echo ""
echo -e "${GREEN}[5/6]${NC} Creating TrueNAS credentials secret..."
kubectl --kubeconfig "$KUBECONFIG_FILE" create namespace kube-system --dry-run=client -o yaml | \
    kubectl --kubeconfig "$KUBECONFIG_FILE" apply -f -

kubectl --kubeconfig "$KUBECONFIG_FILE" create secret generic tns-csi-secret \
    --namespace=kube-system \
    --from-literal=url="$TRUENAS_URL" \
    --from-literal=api-key="$TRUENAS_API_KEY" \
    --dry-run=client -o yaml | \
    kubectl --kubeconfig "$KUBECONFIG_FILE" apply -f -

echo ""
echo -e "${GREEN}[6/6]${NC} Deploying CSI driver with Helm..."

# Check if Helm release exists
if helm --kubeconfig "$KUBECONFIG_FILE" list -n kube-system | grep -q tns-csi; then
    echo "Upgrading existing Helm release..."
    helm upgrade tns-csi charts/tns-csi-driver \
        --kubeconfig "$KUBECONFIG_FILE" \
        --namespace kube-system \
        --set image.repository="$IMAGE_NAME" \
        --set image.tag="$IMAGE_TAG" \
        --set image.pullPolicy=Never \
        --set truenas.url="$TRUENAS_URL" \
        --set truenas.apiKey="$TRUENAS_API_KEY" \
        --set storageClasses.nfs.enabled=true \
        --set storageClasses.nfs.pool="storage" \
        --set storageClasses.nfs.server="$TRUENAS_IP" \
        --set storageClasses.nvmeof.enabled=true \
        --set storageClasses.nvmeof.pool="storage" \
        --set storageClasses.nvmeof.server="$TRUENAS_IP" \
        --wait --timeout 5m
else
    echo "Installing new Helm release..."
    helm install tns-csi charts/tns-csi-driver \
        --kubeconfig "$KUBECONFIG_FILE" \
        --namespace kube-system \
        --create-namespace \
        --set image.repository="$IMAGE_NAME" \
        --set image.tag="$IMAGE_TAG" \
        --set image.pullPolicy=Never \
        --set truenas.url="$TRUENAS_URL" \
        --set truenas.apiKey="$TRUENAS_API_KEY" \
        --set storageClasses.nfs.enabled=true \
        --set storageClasses.nfs.pool="storage" \
        --set storageClasses.nfs.server="$TRUENAS_IP" \
        --set storageClasses.nvmeof.enabled=true \
        --set storageClasses.nvmeof.pool="storage" \
        --set storageClasses.nvmeof.server="$TRUENAS_IP" \
        --wait --timeout 5m
fi

echo ""
echo -e "${GREEN}[Verification]${NC} Checking deployment status..."
echo ""

# Check pods
echo "Pods:"
kubectl --kubeconfig "$KUBECONFIG_FILE" get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

echo ""
echo "Storage Classes:"
kubectl --kubeconfig "$KUBECONFIG_FILE" get storageclass

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Deployment Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Controller logs:"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin"
echo ""
echo "Node logs:"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin"
echo ""
echo "Next: Run ./scripts/test-nvmeof.sh to test NVMe-oF volumes"
echo ""
