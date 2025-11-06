#!/bin/bash
set -e

# Quick deployment script for Kind with TrueNAS CSI Driver
# This script automates the entire setup process

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
CLUSTER_NAME="${CLUSTER_NAME:-truenas-csi-test}"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

info() {
    echo -e "${GREEN}INFO: $1${NC}"
}

warn() {
    echo -e "${YELLOW}WARN: $1${NC}"
}

error() {
    echo -e "${RED}ERROR: $1${NC}" >&2
    exit 1
}

echo "======================================================="
echo "TrueNAS CSI Driver - Kind Deployment Script"
echo "======================================================="
echo

# Check prerequisites
info "Checking prerequisites..."

if ! command -v kind &> /dev/null; then
    error "kind not found. Please install Kind: https://kind.sigs.k8s.io/docs/user/quick-start/"
fi

if ! command -v kubectl &> /dev/null; then
    error "kubectl not found. Please install kubectl"
fi

if ! command -v docker &> /dev/null; then
    error "docker not found. Please install Docker"
fi

# Load credentials from .tns-credentials if it exists
CREDS_FILE="$PROJECT_ROOT/.tns-credentials"
if [ -f "$CREDS_FILE" ]; then
    info "Loading TrueNAS credentials from .tns-credentials"
    source "$CREDS_FILE"
else
    warn "No .tns-credentials file found"
    read -p "Enter TrueNAS WebSocket URL (e.g., wss://10.10.20.100:443/api/current): " TRUENAS_URL
    read -p "Enter TrueNAS API key: " TRUENAS_API_KEY
fi

# Validate credentials
if [ -z "$TRUENAS_URL" ] || [ -z "$TRUENAS_API_KEY" ]; then
    error "TrueNAS URL and API key are required"
fi

# Extract server IP from URL
TRUENAS_SERVER=$(echo "$TRUENAS_URL" | sed -E 's|^wss?://([^:/]+).*|\1|')
info "TrueNAS Server: $TRUENAS_SERVER"

# Check if cluster already exists
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    warn "Cluster '$CLUSTER_NAME' already exists"
    read -p "Delete and recreate? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        info "Deleting existing cluster..."
        kind delete cluster --name "$CLUSTER_NAME"
    else
        info "Using existing cluster"
    fi
fi

# Create Kind cluster if needed
if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    info "Creating Kind cluster..."
    kind create cluster --config "$PROJECT_ROOT/kind-config.yaml"
else
    info "Cluster already exists, skipping creation"
fi

# Setup NFS support
info "Setting up NFS support in Kind nodes..."
"$SCRIPT_DIR/setup-kind-nfs.sh" "$CLUSTER_NAME"

# Build Docker image
info "Building CSI driver Docker image..."
cd "$PROJECT_ROOT"
docker build -t bfenski/tns-csi:v0.0.1 .

# Load image into Kind
info "Loading Docker image into Kind cluster..."
kind load docker-image bfenski/tns-csi:v0.0.1 --name "$CLUSTER_NAME"

# Create namespace if needed
info "Setting up Kubernetes resources..."

# Create secret with TrueNAS credentials
info "Creating TrueNAS credentials secret..."
kubectl create secret generic tns-csi-secret \
    --namespace=kube-system \
    --from-literal=url="$TRUENAS_URL" \
    --from-literal=api-key="$TRUENAS_API_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

# Apply manifests
info "Deploying CSI driver components..."
kubectl apply -f "$PROJECT_ROOT/deploy/rbac.yaml"
kubectl apply -f "$PROJECT_ROOT/deploy/csidriver.yaml"
kubectl apply -f "$PROJECT_ROOT/deploy/controller.yaml"
kubectl apply -f "$PROJECT_ROOT/deploy/node.yaml"

# Apply storage class
info "Creating storage class..."
kubectl apply -f "$PROJECT_ROOT/deploy/storageclass.yaml"

# Wait for pods to be ready
info "Waiting for CSI driver pods to be ready..."
echo "  Waiting for controller..."
kubectl wait --for=condition=ready pod -n kube-system -l app=tns-csi-controller --timeout=120s || warn "Controller pod not ready yet"

echo "  Waiting for node plugins..."
kubectl wait --for=condition=ready pod -n kube-system -l app=tns-csi-node --timeout=120s || warn "Node pods not ready yet"

echo
echo "======================================================="
echo "✓ Deployment Complete!"
echo "======================================================="
echo
info "Cluster information:"
kubectl cluster-info --context "kind-${CLUSTER_NAME}"
echo

info "CSI driver status:"
kubectl get pods -n kube-system -l 'app in (tns-csi-controller,tns-csi-node)'
echo

info "Storage classes:"
kubectl get storageclass
echo

echo "Next steps:"
echo "1. Test with example PVC:"
echo "   kubectl apply -f test-pvc.yaml"
echo
echo "2. Check PVC status:"
echo "   kubectl get pvc"
echo
echo "3. Check controller logs:"
echo "   kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin"
echo
echo "4. Check node logs:"
echo "   kubectl logs -n kube-system -l app=tns-csi-node -c tns-csi-plugin"
echo
