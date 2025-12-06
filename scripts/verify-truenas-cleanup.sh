#!/bin/bash
# TrueNAS Cleanup Verification Script
# Verifies that no orphaned NVMe-oF/NFS resources are left on TrueNAS after tests
#
# Usage:
#   ./verify-truenas-cleanup.sh [--fix]
#
# Options:
#   --fix    Automatically clean up orphaned resources (use with caution)

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIX_MODE=false

# Parse arguments
if [[ "$1" == "--fix" ]]; then
    FIX_MODE=true
    echo -e "${YELLOW}⚠️  FIX MODE ENABLED - Will attempt to clean up orphaned resources${NC}"
    echo ""
fi

# TrueNAS connection details from environment
TRUENAS_HOST="${TRUENAS_HOST:-}"
TRUENAS_API_KEY="${TRUENAS_API_KEY:-}"

if [[ -z "${TRUENAS_HOST}" || -z "${TRUENAS_API_KEY}" ]]; then
    echo -e "${RED}ERROR: TRUENAS_HOST and TRUENAS_API_KEY must be set${NC}"
    echo "Export these environment variables before running this script:"
    echo "  export TRUENAS_HOST=your-truenas-host"
    echo "  export TRUENAS_API_KEY=your-api-key"
    exit 1
fi

echo "========================================"
echo "TrueNAS Cleanup Verification"
echo "========================================"
echo "Host: ${TRUENAS_HOST}"
echo "Fix Mode: ${FIX_MODE}"
echo ""

# Function to call TrueNAS API
call_truenas_api() {
    local method=$1
    local params=${2:-"[]"}
    
    curl -s -k -X POST "https://${TRUENAS_HOST}/api/v2.0/core/call" \
        -H "Authorization: Bearer ${TRUENAS_API_KEY}" \
        -H "Content-Type: application/json" \
        -d "{\"method\": \"${method}\", \"params\": ${params}}"
}

# Track issues found
TOTAL_ISSUES=0
NVMEOF_ORPHANS=0
NFS_ORPHANS=0
ZVOL_ORPHANS=0
DATASET_ORPHANS=0

echo "=== Checking NVMe-oF Subsystems ==="
echo ""

# List all NVMe-oF subsystems (try multiple API methods)
for api_method in "nvmet.subsys.query" "sharing.nvme.query" "nvmet.subsystem.query"; do
    echo "Trying API method: ${api_method}..."
    SUBSYSTEMS=$(call_truenas_api "${api_method}" || echo "[]")
    
    # Check if we got valid JSON
    if echo "${SUBSYSTEMS}" | jq -e . >/dev/null 2>&1; then
        SUBSYS_COUNT=$(echo "${SUBSYSTEMS}" | jq 'length')
        echo -e "${GREEN}✓${NC} Found ${SUBSYS_COUNT} subsystem(s) using ${api_method}"
        
        # Check for CSI-created subsystems (containing "nqn.2137.csi.tns")
        CSI_SUBSYSTEMS=$(echo "${SUBSYSTEMS}" | jq '[.[] | select(.name // .subnqn | contains("nqn.2137.csi.tns"))]')
        CSI_COUNT=$(echo "${CSI_SUBSYSTEMS}" | jq 'length')
        
        if [[ ${CSI_COUNT} -gt 0 ]]; then
            echo -e "${YELLOW}⚠️  Found ${CSI_COUNT} orphaned CSI subsystem(s):${NC}"
            echo "${CSI_SUBSYSTEMS}" | jq -r '.[] | "  - ID: \(.id), NQN: \(.name // .subnqn)"'
            NVMEOF_ORPHANS=$((NVMEOF_ORPHANS + CSI_COUNT))
            TOTAL_ISSUES=$((TOTAL_ISSUES + CSI_COUNT))
            
            # Fix mode: delete orphaned subsystems
            if [[ "${FIX_MODE}" == "true" ]]; then
                echo ""
                echo "Cleaning up orphaned subsystems..."
                echo "${CSI_SUBSYSTEMS}" | jq -r '.[].id' | while read -r subsys_id; do
                    echo "  Deleting subsystem ${subsys_id}..."
                    call_truenas_api "nvmet.subsys.delete" "[${subsys_id}]" >/dev/null
                    echo -e "  ${GREEN}✓${NC} Deleted subsystem ${subsys_id}"
                done
            fi
        else
            echo -e "${GREEN}✓${NC} No orphaned CSI subsystems found"
        fi
        break
    else
        echo -e "${YELLOW}⚠${NC}  Method ${api_method} not available or returned error"
    fi
done

echo ""
echo "=== Checking NVMe-oF Namespaces ==="
echo ""

# List all NVMe-oF namespaces
NAMESPACES=$(call_truenas_api "nvmet.namespace.query" || echo "[]")
if echo "${NAMESPACES}" | jq -e . >/dev/null 2>&1; then
    NS_COUNT=$(echo "${NAMESPACES}" | jq 'length')
    echo -e "${GREEN}✓${NC} Found ${NS_COUNT} namespace(s)"
    
    # Check for CSI-created namespaces (device path containing "pvc-")
    CSI_NAMESPACES=$(echo "${NAMESPACES}" | jq '[.[] | select(.device | contains("pvc-"))]')
    CSI_NS_COUNT=$(echo "${CSI_NAMESPACES}" | jq 'length')
    
    if [[ ${CSI_NS_COUNT} -gt 0 ]]; then
        echo -e "${YELLOW}⚠️  Found ${CSI_NS_COUNT} orphaned CSI namespace(s):${NC}"
        echo "${CSI_NAMESPACES}" | jq -r '.[] | "  - ID: \(.id), Device: \(.device)"'
        NVMEOF_ORPHANS=$((NVMEOF_ORPHANS + CSI_NS_COUNT))
        TOTAL_ISSUES=$((TOTAL_ISSUES + CSI_NS_COUNT))
        
        # Fix mode: delete orphaned namespaces
        if [[ "${FIX_MODE}" == "true" ]]; then
            echo ""
            echo "Cleaning up orphaned namespaces..."
            echo "${CSI_NAMESPACES}" | jq -r '.[].id' | while read -r ns_id; do
                echo "  Deleting namespace ${ns_id}..."
                call_truenas_api "nvmet.namespace.delete" "[${ns_id}]" >/dev/null
                echo -e "  ${GREEN}✓${NC} Deleted namespace ${ns_id}"
            done
        fi
    else
        echo -e "${GREEN}✓${NC} No orphaned CSI namespaces found"
    fi
else
    echo -e "${YELLOW}⚠${NC}  Could not query namespaces"
fi

echo ""
echo "=== Checking NFS Shares ==="
echo ""

# List all NFS shares
NFS_SHARES=$(call_truenas_api "sharing.nfs.query" || echo "[]")
if echo "${NFS_SHARES}" | jq -e . >/dev/null 2>&1; then
    SHARE_COUNT=$(echo "${NFS_SHARES}" | jq 'length')
    echo -e "${GREEN}✓${NC} Found ${SHARE_COUNT} NFS share(s)"
    
    # Check for CSI-created shares (path containing "pvc-")
    CSI_SHARES=$(echo "${NFS_SHARES}" | jq '[.[] | select(.path | contains("pvc-"))]')
    CSI_SHARE_COUNT=$(echo "${CSI_SHARES}" | jq 'length')
    
    if [[ ${CSI_SHARE_COUNT} -gt 0 ]]; then
        echo -e "${YELLOW}⚠️  Found ${CSI_SHARE_COUNT} orphaned CSI NFS share(s):${NC}"
        echo "${CSI_SHARES}" | jq -r '.[] | "  - ID: \(.id), Path: \(.path)"'
        NFS_ORPHANS=$((NFS_ORPHANS + CSI_SHARE_COUNT))
        TOTAL_ISSUES=$((TOTAL_ISSUES + CSI_SHARE_COUNT))
        
        # Fix mode: delete orphaned shares
        if [[ "${FIX_MODE}" == "true" ]]; then
            echo ""
            echo "Cleaning up orphaned NFS shares..."
            echo "${CSI_SHARES}" | jq -r '.[].id' | while read -r share_id; do
                echo "  Deleting NFS share ${share_id}..."
                call_truenas_api "sharing.nfs.delete" "[${share_id}]" >/dev/null
                echo -e "  ${GREEN}✓${NC} Deleted NFS share ${share_id}"
            done
        fi
    else
        echo -e "${GREEN}✓${NC} No orphaned CSI NFS shares found"
    fi
else
    echo -e "${YELLOW}⚠${NC}  Could not query NFS shares"
fi

echo ""
echo "=== Checking Datasets (ZVOLs and Datasets) ==="
echo ""

# List all datasets
DATASETS=$(call_truenas_api "pool.dataset.query" || echo "[]")
if echo "${DATASETS}" | jq -e . >/dev/null 2>&1; then
    DATASET_COUNT=$(echo "${DATASETS}" | jq 'length')
    echo -e "${GREEN}✓${NC} Found ${DATASET_COUNT} dataset(s)"
    
    # Check for CSI-created datasets (name containing "pvc-")
    CSI_DATASETS=$(echo "${DATASETS}" | jq '[.[] | select(.name | contains("pvc-"))]')
    CSI_DATASET_COUNT=$(echo "${CSI_DATASETS}" | jq 'length')
    
    if [[ ${CSI_DATASET_COUNT} -gt 0 ]]; then
        echo -e "${YELLOW}⚠️  Found ${CSI_DATASET_COUNT} orphaned CSI dataset(s)/ZVOL(s):${NC}"
        echo "${CSI_DATASETS}" | jq -r '.[] | "  - ID: \(.id), Name: \(.name), Type: \(.type)"'
        
        # Separate ZVOLs from datasets
        CSI_ZVOLS=$(echo "${CSI_DATASETS}" | jq '[.[] | select(.type == "VOLUME")]')
        CSI_ZVOL_COUNT=$(echo "${CSI_ZVOLS}" | jq 'length')
        CSI_DS=$(echo "${CSI_DATASETS}" | jq '[.[] | select(.type != "VOLUME")]')
        CSI_DS_COUNT=$(echo "${CSI_DS}" | jq 'length')
        
        ZVOL_ORPHANS=$((ZVOL_ORPHANS + CSI_ZVOL_COUNT))
        DATASET_ORPHANS=$((DATASET_ORPHANS + CSI_DS_COUNT))
        TOTAL_ISSUES=$((TOTAL_ISSUES + CSI_DATASET_COUNT))
        
        # Fix mode: delete orphaned datasets
        if [[ "${FIX_MODE}" == "true" ]]; then
            echo ""
            echo "Cleaning up orphaned datasets/ZVOLs..."
            echo "${CSI_DATASETS}" | jq -r '.[].id' | while read -r dataset_id; do
                echo "  Deleting dataset ${dataset_id}..."
                call_truenas_api "pool.dataset.delete" "[\"${dataset_id}\"]" >/dev/null
                echo -e "  ${GREEN}✓${NC} Deleted dataset ${dataset_id}"
            done
        fi
    else
        echo -e "${GREEN}✓${NC} No orphaned CSI datasets/ZVOLs found"
    fi
else
    echo -e "${YELLOW}⚠${NC}  Could not query datasets"
fi

echo ""
echo "========================================"
echo "Summary"
echo "========================================"
echo ""
echo "Total issues found: ${TOTAL_ISSUES}"
echo "  - NVMe-oF orphaned resources: ${NVMEOF_ORPHANS}"
echo "  - NFS orphaned shares: ${NFS_ORPHANS}"
echo "  - Orphaned ZVOLs: ${ZVOL_ORPHANS}"
echo "  - Orphaned datasets: ${DATASET_ORPHANS}"
echo ""

if [[ ${TOTAL_ISSUES} -eq 0 ]]; then
    echo -e "${GREEN}✓ No orphaned resources found - TrueNAS is clean!${NC}"
    exit 0
else
    if [[ "${FIX_MODE}" == "true" ]]; then
        echo -e "${GREEN}✓ Cleanup completed${NC}"
        echo ""
        echo "Re-run without --fix to verify all resources were cleaned up"
        exit 0
    else
        echo -e "${YELLOW}⚠️  Found orphaned resources on TrueNAS${NC}"
        echo ""
        echo "To automatically clean up these resources, run:"
        echo "  $0 --fix"
        echo ""
        echo -e "${RED}WARNING: Use --fix with caution. Ensure these are truly orphaned resources!${NC}"
        exit 1
    fi
fi
