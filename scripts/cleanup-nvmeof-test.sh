#!/bin/bash
# Cleanup script for NVMe-oF testing environment

set -e

VM_NAME="truenas-nvme-test"
KUBECONFIG_FILE="$HOME/.kube/k3s-nvmeof-test"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Cleanup NVMe-oF Test Environment${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Check what to clean up
echo "What would you like to clean up?"
echo ""
echo "1. Delete test resources only (PVCs, Pods)"
echo "2. Uninstall CSI driver (keep VM)"
echo "3. Stop VM (preserve state)"
echo "4. Delete VM completely"
echo "5. Full cleanup (everything)"
echo ""
read -p "Enter choice (1-5): " -n 1 -r
echo ""

case $REPLY in
    1)
        echo -e "${YELLOW}Deleting test resources...${NC}"
        if [ -f "$KUBECONFIG_FILE" ]; then
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nvmeof-pod --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nvmeof-pvc --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nfs-pod --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nfs-pvc --ignore-not-found=true
            echo -e "${GREEN}✓${NC} Test resources deleted"
        else
            echo -e "${YELLOW}Kubeconfig not found, skipping${NC}"
        fi
        ;;
    2)
        echo -e "${YELLOW}Uninstalling CSI driver...${NC}"
        if [ -f "$KUBECONFIG_FILE" ]; then
            # Delete test resources first
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nvmeof-pod --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nvmeof-pvc --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nfs-pod --ignore-not-found=true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nfs-pvc --ignore-not-found=true
            
            # Uninstall Helm release
            helm --kubeconfig "$KUBECONFIG_FILE" uninstall tns-csi -n kube-system --ignore-not-found 2>/dev/null || true
            echo -e "${GREEN}✓${NC} CSI driver uninstalled"
        else
            echo -e "${YELLOW}Kubeconfig not found, skipping${NC}"
        fi
        ;;
    3)
        echo -e "${YELLOW}Stopping VM...${NC}"
        if multipass list | grep -q "$VM_NAME"; then
            multipass stop "$VM_NAME"
            echo -e "${GREEN}✓${NC} VM stopped (can be restarted with: multipass start $VM_NAME)"
        else
            echo -e "${YELLOW}VM not found${NC}"
        fi
        ;;
    4)
        echo -e "${YELLOW}Deleting VM...${NC}"
        if multipass list | grep -q "$VM_NAME"; then
            multipass delete "$VM_NAME"
            multipass purge
            echo -e "${GREEN}✓${NC} VM deleted"
        else
            echo -e "${YELLOW}VM not found${NC}"
        fi
        
        # Remove kubeconfig
        if [ -f "$KUBECONFIG_FILE" ]; then
            rm "$KUBECONFIG_FILE"
            [ -f "${KUBECONFIG_FILE}.bak" ] && rm "${KUBECONFIG_FILE}.bak"
            echo -e "${GREEN}✓${NC} Kubeconfig removed"
        fi
        ;;
    5)
        echo -e "${YELLOW}Full cleanup...${NC}"
        
        # Delete test resources
        if [ -f "$KUBECONFIG_FILE" ]; then
            echo "Deleting test resources..."
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nvmeof-pod --ignore-not-found=true 2>/dev/null || true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nvmeof-pvc --ignore-not-found=true 2>/dev/null || true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pod test-nfs-pod --ignore-not-found=true 2>/dev/null || true
            kubectl --kubeconfig "$KUBECONFIG_FILE" delete pvc test-nfs-pvc --ignore-not-found=true 2>/dev/null || true
            
            # Uninstall Helm release
            echo "Uninstalling CSI driver..."
            helm --kubeconfig "$KUBECONFIG_FILE" uninstall tns-csi -n kube-system --ignore-not-found 2>/dev/null || true
        fi
        
        # Delete VM
        if multipass list | grep -q "$VM_NAME"; then
            echo "Deleting VM..."
            multipass delete "$VM_NAME"
            multipass purge
        fi
        
        # Remove kubeconfig
        if [ -f "$KUBECONFIG_FILE" ]; then
            rm "$KUBECONFIG_FILE"
            [ -f "${KUBECONFIG_FILE}.bak" ] && rm "${KUBECONFIG_FILE}.bak"
        fi
        
        echo -e "${GREEN}✓${NC} Full cleanup complete"
        ;;
    *)
        echo "Invalid choice"
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}Cleanup complete!${NC}"
echo ""
