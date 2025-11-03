#!/bin/bash
set -e

# NVMe-oF Prerequisites Checker
# This script helps verify that TrueNAS and Kubernetes nodes are properly configured for NVMe-oF

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info() {
    echo -e "${GREEN}✓${NC} $1"
}

warn() {
    echo -e "${YELLOW}⚠${NC} $1"
}

error() {
    echo -e "${RED}✗${NC} $1"
}

title() {
    echo -e "\n${BLUE}=== $1 ===${NC}\n"
}

# Usage
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Check NVMe-oF prerequisites for TrueNAS CSI driver.

OPTIONS:
  -h, --help              Show this help message
  -u, --url URL           TrueNAS WebSocket API URL (e.g., wss://10.10.20.100:443/api/current)
  -k, --api-key KEY       TrueNAS API key
  -n, --nodes             Check Kubernetes nodes for NVMe-oF support (requires kubectl)
  --skip-truenas          Skip TrueNAS checks
  --skip-nodes            Skip Kubernetes node checks

EXAMPLES:
  # Check TrueNAS configuration
  $0 -u wss://truenas.local:443/api/current -k "YOUR-API-KEY"

  # Check Kubernetes nodes only
  $0 --nodes --skip-truenas

  # Check both TrueNAS and nodes
  $0 -u wss://truenas.local:443/api/current -k "YOUR-API-KEY" --nodes

EOF
    exit 0
}

# Parse arguments
TRUENAS_URL=""
API_KEY=""
CHECK_NODES=false
SKIP_TRUENAS=false
SKIP_NODES=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            ;;
        -u|--url)
            TRUENAS_URL="$2"
            shift 2
            ;;
        -k|--api-key)
            API_KEY="$2"
            shift 2
            ;;
        -n|--nodes)
            CHECK_NODES=true
            shift
            ;;
        --skip-truenas)
            SKIP_TRUENAS=true
            shift
            ;;
        --skip-nodes)
            SKIP_NODES=true
            shift
            ;;
        *)
            error "Unknown option: $1"
            usage
            ;;
    esac
done

# Check dependencies
title "Checking Dependencies"

if ! command -v jq &> /dev/null; then
    error "jq is not installed. Install it with: sudo apt-get install jq"
    exit 1
fi
info "jq is installed"

if ! command -v curl &> /dev/null; then
    error "curl is not installed. Install it with: sudo apt-get install curl"
    exit 1
fi
info "curl is installed"

if $CHECK_NODES && ! $SKIP_NODES; then
    if ! command -v kubectl &> /dev/null; then
        error "kubectl is not installed"
        exit 1
    fi
    info "kubectl is installed"
fi

# Check TrueNAS NVMe-oF configuration
if ! $SKIP_TRUENAS; then
    title "Checking TrueNAS NVMe-oF Configuration"
    
    if [[ -z "$TRUENAS_URL" ]] || [[ -z "$API_KEY" ]]; then
        warn "TrueNAS URL and API key not provided. Skipping TrueNAS checks."
        warn "Use -u and -k flags to provide TrueNAS connection details."
    else
        info "Connecting to TrueNAS: $TRUENAS_URL"
        
        # Extract host and port from WebSocket URL
        # Convert wss://host:port/api/current to https://host:port/api/v2.0
        HOST=$(echo "$TRUENAS_URL" | sed -E 's/wss?:\/\/([^:\/]+).*/\1/')
        PORT=$(echo "$TRUENAS_URL" | sed -E 's/.*:([0-9]+).*/\1/')
        API_ENDPOINT="https://${HOST}:${PORT}/api/v2.0"
        
        info "API Endpoint: $API_ENDPOINT"
        
        # Check NVMe-oF service status
        echo ""
        echo "Checking NVMe-oF service..."
        SERVICE_STATUS=$(curl -k -s -X GET \
            -H "Authorization: Bearer ${API_KEY}" \
            "${API_ENDPOINT}/service?service=nvmeof" | jq -r '.[0].state' 2>/dev/null || echo "error")
        
        if [[ "$SERVICE_STATUS" == "RUNNING" ]]; then
            info "NVMe-oF service is running"
        elif [[ "$SERVICE_STATUS" == "STOPPED" ]]; then
            error "NVMe-oF service is stopped"
            echo "   To enable: System Settings → Services → NVMe-oF → Toggle ON"
            exit 1
        else
            error "Could not check NVMe-oF service status"
            echo "   API response: $SERVICE_STATUS"
            exit 1
        fi
        
        # Check NVMe-oF portals/ports
        echo ""
        echo "Checking NVMe-oF portals..."
        
        # Query for NVMe-oF portals - this endpoint may vary by TrueNAS version
        PORTALS=$(curl -k -s -X POST \
            -H "Authorization: Bearer ${API_KEY}" \
            -H "Content-Type: application/json" \
            -d '{"method": "nvmeof.port.query", "params": []}' \
            "${API_ENDPOINT}/core/ping" 2>/dev/null || echo "[]")
        
        PORTAL_COUNT=$(echo "$PORTALS" | jq '. | length' 2>/dev/null || echo "0")
        
        if [[ "$PORTAL_COUNT" -gt 0 ]]; then
            info "Found $PORTAL_COUNT NVMe-oF portal(s)"
            echo "$PORTALS" | jq -r '.[] | "   - \(.addr_trtype) on \(.addr_traddr):\(.addr_trsvcid)"'
            
            # Check for TCP portal
            TCP_COUNT=$(echo "$PORTALS" | jq '[.[] | select(.addr_trtype == "TCP")] | length' 2>/dev/null || echo "0")
            if [[ "$TCP_COUNT" -gt 0 ]]; then
                info "Found TCP portal (required for CSI driver)"
            else
                error "No TCP portal found"
                echo "   The CSI driver requires at least one TCP portal"
            fi
        else
            error "No NVMe-oF portals configured"
            echo ""
            echo "   ⚠️  This is a REQUIRED step before provisioning NVMe-oF volumes"
            echo ""
            echo "   To configure:"
            echo "   1. Log in to TrueNAS web UI"
            echo "   2. Navigate to: Sharing → Block Shares (NVMe-oF)"
            echo "   3. Click 'Portals' tab"
            echo "   4. Click 'Add'"
            echo "   5. Configure:"
            echo "      - Listen Address: 0.0.0.0 (or specific interface)"
            echo "      - Port: 4420"
            echo "      - Transport: TCP"
            echo "   6. Click 'Save'"
            echo ""
            exit 1
        fi
    fi
fi

# Check Kubernetes nodes
if $CHECK_NODES && ! $SKIP_NODES; then
    title "Checking Kubernetes Nodes for NVMe-oF Support"
    
    # Get all nodes
    NODES=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')
    
    if [[ -z "$NODES" ]]; then
        error "No Kubernetes nodes found"
        exit 1
    fi
    
    info "Found nodes: $NODES"
    echo ""
    
    ALL_READY=true
    
    for NODE in $NODES; do
        echo "Checking node: $NODE"
        
        # Check if nvme-cli is installed
        NVME_CLI=$(kubectl debug "node/${NODE}" -it --image=busybox -- sh -c "which nvme" 2>/dev/null || echo "")
        if [[ -n "$NVME_CLI" ]]; then
            info "  nvme-cli is installed"
        else
            error "  nvme-cli is NOT installed"
            echo "     Install with: sudo apt-get install -y nvme-cli"
            ALL_READY=false
        fi
        
        # Check if nvme-tcp module is loaded
        NVME_TCP=$(kubectl debug "node/${NODE}" -it --image=busybox -- sh -c "lsmod | grep nvme_tcp" 2>/dev/null || echo "")
        if [[ -n "$NVME_TCP" ]]; then
            info "  nvme-tcp module is loaded"
        else
            warn "  nvme-tcp module is NOT loaded"
            echo "     Load with: sudo modprobe nvme-tcp"
            echo "     Make persistent: echo 'nvme-tcp' | sudo tee /etc/modules-load.d/nvme.conf"
            ALL_READY=false
        fi
        
        echo ""
    done
    
    if ! $ALL_READY; then
        error "Some nodes are not ready for NVMe-oF"
        exit 1
    else
        info "All nodes are ready for NVMe-oF"
    fi
fi

# Summary
title "Prerequisites Check Summary"

if ! $SKIP_TRUENAS && [[ -n "$TRUENAS_URL" ]]; then
    info "TrueNAS NVMe-oF is properly configured"
fi

if $CHECK_NODES && ! $SKIP_NODES; then
    info "Kubernetes nodes are ready for NVMe-oF"
fi

echo ""
info "All checks passed! You can now provision NVMe-oF volumes."
echo ""
echo "Next steps:"
echo "1. Deploy the CSI driver with NVMe-oF StorageClass enabled"
echo "2. Create a PVC with storageClassName: tns-nvmeof"
echo "3. Check the documentation for examples: docs/QUICKSTART-NVMEOF.md"
echo ""
