#!/bin/bash
# Setup script for local NVMe-oF testing using Multipass VM
# This creates an Ubuntu VM with k3s and NVMe-oF support

set -e

VM_NAME="truenas-nvme-test"
VM_CPUS=4
VM_MEMORY="4G"
VM_DISK="50G"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}TrueNAS CSI - NVMe-oF Testing VM Setup${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Check if multipass is installed
if ! command -v multipass &> /dev/null; then
    echo -e "${RED}Error: multipass is not installed${NC}"
    echo "Install it with: brew install multipass"
    exit 1
fi

# Check if VM already exists
if multipass list | grep -q "$VM_NAME"; then
    echo -e "${YELLOW}VM '$VM_NAME' already exists${NC}"
    read -p "Do you want to delete and recreate it? (y/N): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo "Deleting existing VM..."
        multipass delete "$VM_NAME"
        multipass purge
    else
        echo "Using existing VM"
        exit 0
    fi
fi

echo -e "${GREEN}[1/6]${NC} Launching Ubuntu VM..."
multipass launch 22.04 \
    --name "$VM_NAME" \
    --cpus "$VM_CPUS" \
    --memory "$VM_MEMORY" \
    --disk "$VM_DISK" \
    --timeout 600

echo ""
echo -e "${GREEN}[2/6]${NC} Installing system updates and NVMe tools..."
multipass exec "$VM_NAME" -- bash -c '
    sudo apt-get update -qq
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nvme-cli curl > /dev/null 2>&1
'

echo ""
echo -e "${GREEN}[3/6]${NC} Loading NVMe-oF kernel modules..."
multipass exec "$VM_NAME" -- bash -c '
    sudo modprobe nvme-tcp
    sudo modprobe nvme-fabrics
    echo "nvme-tcp" | sudo tee -a /etc/modules > /dev/null
    echo "nvme-fabrics" | sudo tee -a /etc/modules > /dev/null
'

echo ""
echo -e "${GREEN}[4/6]${NC} Installing k3s (lightweight Kubernetes)..."
multipass exec "$VM_NAME" -- bash -c '
    curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644 > /dev/null 2>&1
    # Wait for k3s to be ready
    sudo systemctl enable k3s > /dev/null 2>&1
    sleep 10
    until sudo kubectl get nodes 2>/dev/null | grep -q Ready; do
        echo "Waiting for k3s to be ready..."
        sleep 5
    done
'

echo ""
echo -e "${GREEN}[5/6]${NC} Configuring kubectl access from macOS..."
# Get VM IP
VM_IP=$(multipass info "$VM_NAME" | grep IPv4 | awk '{print $2}')
echo "VM IP address: $VM_IP"

# Export kubeconfig
KUBECONFIG_DIR="$HOME/.kube"
KUBECONFIG_FILE="$KUBECONFIG_DIR/k3s-nvmeof-test"

mkdir -p "$KUBECONFIG_DIR"
multipass exec "$VM_NAME" -- sudo cat /etc/rancher/k3s/k3s.yaml > "$KUBECONFIG_FILE"

# Update server address
sed -i.bak "s|https://127.0.0.1:6443|https://${VM_IP}:6443|g" "$KUBECONFIG_FILE"
rm "${KUBECONFIG_FILE}.bak"

echo ""
echo -e "${GREEN}[6/6]${NC} Verifying setup..."

# Test kubectl connectivity
if kubectl --kubeconfig "$KUBECONFIG_FILE" get nodes > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} kubectl connection successful"
else
    echo -e "${RED}✗${NC} kubectl connection failed"
    exit 1
fi

# Verify NVMe tools
if multipass exec "$VM_NAME" -- nvme version > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} NVMe CLI installed"
else
    echo -e "${RED}✗${NC} NVMe CLI not found"
    exit 1
fi

# Verify kernel modules
if multipass exec "$VM_NAME" -- lsmod | grep -q nvme_tcp; then
    echo -e "${GREEN}✓${NC} NVMe-oF kernel modules loaded"
else
    echo -e "${RED}✗${NC} NVMe-oF kernel modules not loaded"
    exit 1
fi

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Setup Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "VM Name: $VM_NAME"
echo "VM IP: $VM_IP"
echo "Kubeconfig: $KUBECONFIG_FILE"
echo ""
echo "Usage:"
echo "  # Shell into VM:"
echo "  multipass shell $VM_NAME"
echo ""
echo "  # Use kubectl from macOS:"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE get nodes"
echo ""
echo "  # Or set as default:"
echo "  export KUBECONFIG=$KUBECONFIG_FILE"
echo ""
echo "Next steps:"
echo "  1. Run: ./scripts/deploy-nvmeof-test.sh"
echo "  2. Test: ./scripts/test-nvmeof.sh"
echo ""
