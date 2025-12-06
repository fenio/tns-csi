#!/bin/bash
# Universal cleanup script for TrueNAS Scale
# Removes datasets and shares from a specified pool
# 
# Usage:
#   ./cleanup-all-truenas-resources.sh              # Safe mode: Only CSI test artifacts
#   ./cleanup-all-truenas-resources.sh --all        # Remove ALL datasets in pool (DANGEROUS!)
#   ./cleanup-all-truenas-resources.sh --dry-run    # Show what would be deleted

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Parse arguments
MODE="safe"
DRY_RUN=false

for arg in "$@"; do
    case $arg in
        --all)
            MODE="all"
            ;;
        --dry-run)
            DRY_RUN=true
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --all       Remove ALL datasets and shares in the pool (DANGEROUS!)"
            echo "  --dry-run   Show what would be deleted without actually deleting"
            echo "  --help      Show this help message"
            echo ""
            echo "Default mode (safe): Only removes CSI test artifacts (pvc-*, test-csi*)"
            echo ""
            echo "Required environment variables:"
            echo "  TRUENAS_HOST      - TrueNAS hostname/IP"
            echo "  TRUENAS_API_KEY   - API key for authentication"
            echo "  TRUENAS_POOL      - Pool name to clean"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $arg${NC}"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Check required environment variables
if [[ -z "${TRUENAS_HOST}" ]]; then
    echo -e "${RED}Error: TRUENAS_HOST environment variable not set${NC}"
    exit 1
fi

if [[ -z "${TRUENAS_API_KEY}" ]]; then
    echo -e "${RED}Error: TRUENAS_API_KEY environment variable not set${NC}"
    exit 1
fi

if [[ -z "${TRUENAS_POOL}" ]]; then
    echo -e "${RED}Error: TRUENAS_POOL environment variable not set${NC}"
    exit 1
fi

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}TrueNAS Cleanup Script${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo -e "${YELLOW}Host:${NC} ${TRUENAS_HOST}"
echo -e "${YELLOW}Pool:${NC} ${TRUENAS_POOL}"
echo -e "${YELLOW}Mode:${NC} ${MODE}"
if [ "$DRY_RUN" = true ]; then
    echo -e "${YELLOW}Dry Run:${NC} Enabled (no changes will be made)"
fi
echo ""

if [ "$MODE" = "all" ]; then
    echo -e "${RED}⚠️  WARNING: You are about to delete ALL datasets and shares in pool '${TRUENAS_POOL}'${NC}"
    echo -e "${RED}⚠️  This operation cannot be undone!${NC}"
    echo ""
    read -p "Type 'DELETE ALL' to confirm: " CONFIRM
    if [ "$CONFIRM" != "DELETE ALL" ]; then
        echo -e "${YELLOW}Cancelled by user${NC}"
        exit 0
    fi
fi

# Create a Go script to interact with TrueNAS API
cat > /tmp/truenas-cleanup-all.go <<'EOFGO'
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func main() {
	host := os.Getenv("TRUENAS_HOST")
	apiKey := os.Getenv("TRUENAS_API_KEY")
	pool := os.Getenv("TRUENAS_POOL")
	mode := os.Getenv("CLEANUP_MODE")
	dryRun := os.Getenv("DRY_RUN") == "true"

	if host == "" || apiKey == "" || pool == "" {
		fmt.Println("Error: Missing required environment variables")
		os.Exit(1)
	}

	// Construct WebSocket URL
	url := fmt.Sprintf("wss://%s/api/current", host)
	
	fmt.Printf("Connecting to TrueNAS at %s...\n", url)
	
	client, err := tnsapi.NewClient(url, apiKey, true)
	if err != nil {
		fmt.Printf("Failed to create client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx := context.Background()

	// List all datasets in the pool
	fmt.Println("\n=== Listing datasets ===")
	var datasets []map[string]interface{}
	if err := client.Call(ctx, "pool.dataset.query", []interface{}{}, &datasets); err != nil {
		fmt.Printf("Failed to query datasets: %v\n", err)
		os.Exit(1)
	}

	targetDatasets := []string{}
	for _, ds := range datasets {
		name, ok := ds["name"].(string)
		if !ok {
			continue
		}
		
		dsType, _ := ds["type"].(string) // FILESYSTEM or VOLUME
		
		// Only include datasets in the specified pool (but not the pool itself)
		if !strings.HasPrefix(name, pool+"/") || name == pool {
			continue
		}
		
		// Filter based on mode
		shouldInclude := false
		if mode == "all" {
			shouldInclude = true
		} else {
			// Safe mode: only CSI test artifacts
			if strings.Contains(name, "pvc-") || strings.Contains(name, "test-csi") {
				shouldInclude = true
			}
		}
		
		if shouldInclude {
			targetDatasets = append(targetDatasets, name)
			typeStr := "dataset"
			if dsType == "VOLUME" {
				typeStr = "ZVOL"
			}
			fmt.Printf("  Found %s: %s\n", typeStr, name)
		}
	}

	if len(targetDatasets) == 0 {
		fmt.Println("\n✓ No datasets found matching criteria - TrueNAS is clean!")
		return
	}

	fmt.Printf("\n=== Found %d dataset(s) to delete ===\n", len(targetDatasets))

	// List NFS shares
	fmt.Println("\n=== Listing NFS shares ===")
	var shares []map[string]interface{}
	if err := client.Call(ctx, "sharing.nfs.query", []interface{}{}, &shares); err != nil {
		fmt.Printf("Failed to query NFS shares: %v\n", err)
		os.Exit(1)
	}

	targetShares := []map[string]interface{}{}
	for _, share := range shares {
		path, ok := share["path"].(string)
		if !ok {
			continue
		}
		
		// Check if share path matches any target dataset
		shouldInclude := false
		
		if mode == "all" {
			// In "all" mode, delete shares for any dataset in the pool
			if strings.HasPrefix(path, "/mnt/"+pool+"/") {
				shouldInclude = true
			}
		} else {
			// Safe mode: only shares for CSI test datasets
			for _, dsName := range targetDatasets {
				if strings.Contains(path, dsName) {
					shouldInclude = true
					break
				}
			}
		}
		
		if shouldInclude {
			targetShares = append(targetShares, share)
			shareID := share["id"]
			fmt.Printf("  Found NFS share: %s (ID: %v)\n", path, shareID)
		}
	}

	if dryRun {
		fmt.Println("\n=== DRY RUN - No changes will be made ===")
		fmt.Printf("Would delete %d NFS share(s)\n", len(targetShares))
		fmt.Printf("Would delete %d dataset(s)\n", len(targetDatasets))
		return
	}

	// Delete NFS shares first
	if len(targetShares) > 0 {
		fmt.Printf("\n=== Deleting %d NFS share(s) ===\n", len(targetShares))
		for _, share := range targetShares {
			shareID := share["id"]
			path := share["path"].(string)
			fmt.Printf("  Deleting NFS share: %s (ID: %v)...\n", path, shareID)
			
			var result interface{}
			if err := client.Call(ctx, "sharing.nfs.delete", []interface{}{shareID}, &result); err != nil {
				fmt.Printf("    ⚠ Failed to delete NFS share: %v\n", err)
			} else {
				fmt.Printf("    ✓ Deleted\n")
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No NFS shares to delete ===")
	}

	// List iSCSI targets (for NVMe-oF)
	fmt.Println("\n=== Listing iSCSI/NVMe-oF targets ===")
	var targets []map[string]interface{}
	if err := client.Call(ctx, "iscsi.target.query", []interface{}{}, &targets); err != nil {
		fmt.Printf("Warning: Failed to query iSCSI targets: %v\n", err)
	} else {
		targetTargets := []map[string]interface{}{}
		for _, target := range targets {
			name, ok := target["name"].(string)
			if !ok {
				continue
			}
			
			shouldInclude := false
			if mode == "all" {
				// In "all" mode, consider all targets (but be cautious)
				if strings.Contains(name, "pvc-") || strings.Contains(name, "test-") {
					shouldInclude = true
				}
			} else {
				// Safe mode: only CSI test targets
				if strings.Contains(name, "pvc-") || strings.Contains(name, "test-csi") {
					shouldInclude = true
				}
			}
			
			if shouldInclude {
				targetTargets = append(targetTargets, target)
				targetID := target["id"]
				fmt.Printf("  Found target: %s (ID: %v)\n", name, targetID)
			}
		}
		
		// NOTE: We don't automatically delete targets as they might be pre-configured infrastructure
		// Instead, we just report them
		if len(targetTargets) > 0 {
			fmt.Printf("\n⚠️  Found %d iSCSI/NVMe-oF target(s)\n", len(targetTargets))
			fmt.Println("Note: Targets are not automatically deleted. Manage them manually if needed.")
		}
	}

	// Delete datasets
	fmt.Printf("\n=== Deleting %d dataset(s) ===\n", len(targetDatasets))
	successCount := 0
	failCount := 0
	
	for _, dsName := range targetDatasets {
		fmt.Printf("  Deleting dataset: %s...\n", dsName)
		
		var result interface{}
		params := []interface{}{
			dsName,
			map[string]interface{}{
				"recursive": true,
				"force":     true,
			},
		}
		
		if err := client.Call(ctx, "pool.dataset.delete", params, &result); err != nil {
			fmt.Printf("    ⚠ Failed to delete dataset: %v\n", err)
			failCount++
		} else {
			fmt.Printf("    ✓ Deleted\n")
			successCount++
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println("\n=== Summary ===")
	fmt.Printf("Successfully deleted: %d dataset(s)\n", successCount)
	if failCount > 0 {
		fmt.Printf("Failed to delete: %d dataset(s)\n", failCount)
	}
	fmt.Println("\n✓ Cleanup complete!")
}
EOFGO

# Build and run the cleanup script
echo -e "${YELLOW}Building cleanup tool...${NC}"

# Store current directory
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLEANUP_DIR=$(mktemp -d)

# Copy the Go script to a temporary directory
cp /tmp/truenas-cleanup-all.go "$CLEANUP_DIR/"
cd "$CLEANUP_DIR"

# Initialize Go module with proper replace directive
go mod init cleanup
go mod edit -replace github.com/fenio/tns-csi="$SCRIPT_DIR"
go mod tidy

echo -e "${YELLOW}Running cleanup...${NC}"
echo ""

export CLEANUP_MODE="$MODE"
export DRY_RUN="$DRY_RUN"

go run truenas-cleanup-all.go

# Cleanup
cd "$SCRIPT_DIR"
rm -rf "$CLEANUP_DIR"
rm -f /tmp/truenas-cleanup-all.go

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Cleanup Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
