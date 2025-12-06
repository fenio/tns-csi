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
		fmt.Println("  No datasets found matching criteria")
	} else {
		fmt.Printf("\n=== Found %d dataset(s) to delete ===\n", len(targetDatasets))
	}

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

	// Delete NFS shares first
	nfsSuccessCount := 0
	nfsFailCount := 0
	if len(targetShares) > 0 {
		fmt.Printf("\n=== Deleting %d NFS share(s) ===\n", len(targetShares))
		for _, share := range targetShares {
			shareID := share["id"]
			path := share["path"].(string)
			fmt.Printf("  Deleting NFS share: %s (ID: %v)...\n", path, shareID)
			
			var result interface{}
			if err := client.Call(ctx, "sharing.nfs.delete", []interface{}{shareID}, &result); err != nil {
				fmt.Printf("    ⚠ Failed to delete NFS share: %v\n", err)
				nfsFailCount++
			} else {
				fmt.Printf("    ✓ Deleted\n")
				nfsSuccessCount++
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No NFS shares to delete ===")
	}

	// List NVMe-oF namespaces
	fmt.Println("\n=== Listing NVMe-oF namespaces ===")
	namespaces, err := client.QueryAllNVMeOFNamespaces(ctx)
	targetNamespaces := []tnsapi.NVMeOFNamespace{}
	if err != nil {
		fmt.Printf("Warning: Failed to query NVMe-oF namespaces: %v\n", err)
	} else {
		for _, ns := range namespaces {
			device := ns.Device
			if device == "" {
				device = ns.DevicePath
			}
			
			shouldInclude := false
			if mode == "all" {
				// In "all" mode, delete namespaces for any dataset in the pool
				if strings.Contains(device, pool+"/") {
					shouldInclude = true
				}
			} else {
				// Safe mode: only CSI test namespaces
				if strings.Contains(device, "pvc-") || strings.Contains(device, "test-csi") {
					shouldInclude = true
				}
			}
			
			if shouldInclude {
				targetNamespaces = append(targetNamespaces, ns)
				fmt.Printf("  Found namespace: ID=%d, Device=%s, SubsystemID=%d, NSID=%d\n", 
					ns.ID, device, ns.Subsystem, ns.NSID)
			}
		}
	}

	// List NVMe-oF subsystems
	fmt.Println("\n=== Listing NVMe-oF subsystems ===")
	subsystems, err := client.ListAllNVMeOFSubsystems(ctx)
	targetSubsystems := []tnsapi.NVMeOFSubsystem{}
	if err != nil {
		fmt.Printf("Warning: Failed to query NVMe-oF subsystems: %v\n", err)
	} else {
		for _, ss := range subsystems {
			shouldInclude := false
			if mode == "all" {
				// In "all" mode, delete CSI-managed subsystems (nqn.2014-08.org.truenas:csi-*)
				if strings.Contains(ss.NQN, "csi-") || strings.Contains(ss.Name, "pvc-") || strings.Contains(ss.Name, "test-") {
					shouldInclude = true
				}
			} else {
				// Safe mode: only CSI test subsystems
				if strings.Contains(ss.NQN, "csi-pvc-") || strings.Contains(ss.Name, "pvc-") || strings.Contains(ss.Name, "test-csi") {
					shouldInclude = true
				}
			}
			
			if shouldInclude {
				targetSubsystems = append(targetSubsystems, ss)
				fmt.Printf("  Found subsystem: ID=%d, Name=%s, NQN=%s\n", ss.ID, ss.Name, ss.NQN)
			}
		}
	}

	if dryRun {
		fmt.Println("\n=== DRY RUN - No changes will be made ===")
		fmt.Printf("Would delete %d NFS share(s)\n", len(targetShares))
		fmt.Printf("Would delete %d NVMe-oF namespace(s)\n", len(targetNamespaces))
		fmt.Printf("Would delete %d NVMe-oF subsystem(s)\n", len(targetSubsystems))
		fmt.Printf("Would delete %d dataset(s)\n", len(targetDatasets))
		return
	}

	// Delete NVMe-oF namespaces first (must be deleted before subsystems)
	nsSuccessCount := 0
	nsFailCount := 0
	if len(targetNamespaces) > 0 {
		fmt.Printf("\n=== Deleting %d NVMe-oF namespace(s) ===\n", len(targetNamespaces))
		for _, ns := range targetNamespaces {
			device := ns.Device
			if device == "" {
				device = ns.DevicePath
			}
			fmt.Printf("  Deleting namespace: ID=%d, Device=%s...\n", ns.ID, device)
			
			if err := client.DeleteNVMeOFNamespace(ctx, ns.ID); err != nil {
				fmt.Printf("    ⚠ Failed to delete namespace: %v\n", err)
				nsFailCount++
			} else {
				fmt.Printf("    ✓ Deleted\n")
				nsSuccessCount++
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No NVMe-oF namespaces to delete ===")
	}

	// Delete NVMe-oF subsystems (after namespaces are deleted)
	// First, query ALL port-subsystem bindings upfront
	fmt.Println("\n=== Querying all port-subsystem bindings ===")
	var allPortBindings []map[string]interface{}
	if err := client.Call(ctx, "nvmet.port_subsys.query", []interface{}{}, &allPortBindings); err != nil {
		fmt.Printf("Warning: Failed to query port-subsystem bindings: %v\n", err)
	} else {
		fmt.Printf("  Found %d total port-subsystem binding(s)\n", len(allPortBindings))
		// Debug: print first binding structure
		if len(allPortBindings) > 0 {
			fmt.Printf("  Debug - First binding keys: ")
			for k := range allPortBindings[0] {
				fmt.Printf("%s, ", k)
			}
			fmt.Println()
			// Print subsystem field specifically
			if subsys, ok := allPortBindings[0]["subsystem"]; ok {
				fmt.Printf("  Debug - subsystem field type: %T, value: %v\n", subsys, subsys)
			}
		}
	}

	// Build a map of subsystem ID -> port binding IDs
	subsysToBindings := make(map[int][]int)
	for _, binding := range allPortBindings {
		bindingID := 0
		subsysID := 0
		
		// Extract binding ID
		if id, ok := binding["id"].(float64); ok {
			bindingID = int(id)
		}
		
		// Extract subsystem ID - the field is "subsys", can be int or nested object
		if subsys, ok := binding["subsys"].(map[string]interface{}); ok {
			if id, ok := subsys["id"].(float64); ok {
				subsysID = int(id)
			}
		} else if id, ok := binding["subsys"].(float64); ok {
			subsysID = int(id)
		}
		
		if bindingID != 0 && subsysID != 0 {
			subsysToBindings[subsysID] = append(subsysToBindings[subsysID], bindingID)
		}
	}
	
	fmt.Printf("  Debug - Built map with %d unique subsystem IDs\n", len(subsysToBindings))

	ssSuccessCount := 0
	ssFailCount := 0
	portBindingCount := 0
	if len(targetSubsystems) > 0 {
		fmt.Printf("\n=== Removing port bindings and deleting %d NVMe-oF subsystem(s) ===\n", len(targetSubsystems))
		for _, ss := range targetSubsystems {
			fmt.Printf("  Processing subsystem: ID=%d, NQN=%s\n", ss.ID, ss.NQN)
			
			// Get port bindings for this subsystem from our map
			bindingIDs := subsysToBindings[ss.ID]
			if len(bindingIDs) > 0 {
				fmt.Printf("    Found %d port binding(s) to remove\n", len(bindingIDs))
				for _, bindingID := range bindingIDs {
					fmt.Printf("      Removing port binding ID=%d...\n", bindingID)
					if err := client.RemoveSubsystemFromPort(ctx, bindingID); err != nil {
						fmt.Printf("        ⚠ Failed to remove port binding: %v\n", err)
					} else {
						fmt.Printf("        ✓ Removed\n")
						portBindingCount++
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			
			// Now delete the subsystem
			fmt.Printf("    Deleting subsystem...\n")
			if err := client.DeleteNVMeOFSubsystem(ctx, ss.ID); err != nil {
				fmt.Printf("    ⚠ Failed to delete subsystem: %v\n", err)
				ssFailCount++
			} else {
				fmt.Printf("    ✓ Deleted\n")
				ssSuccessCount++
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No NVMe-oF subsystems to delete ===")
	}

	// Delete datasets
	successCount := 0
	failCount := 0
	if len(targetDatasets) > 0 {
		fmt.Printf("\n=== Deleting %d dataset(s) ===\n", len(targetDatasets))
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
	} else {
		fmt.Println("\n=== No datasets to delete ===")
	}

	fmt.Println("\n=== Summary ===")
	fmt.Printf("NFS shares:         %d deleted, %d failed\n", nfsSuccessCount, nfsFailCount)
	fmt.Printf("NVMe-oF namespaces: %d deleted, %d failed\n", nsSuccessCount, nsFailCount)
	fmt.Printf("NVMe-oF port bindings: %d removed\n", portBindingCount)
	fmt.Printf("NVMe-oF subsystems: %d deleted, %d failed\n", ssSuccessCount, ssFailCount)
	fmt.Printf("Datasets:           %d deleted, %d failed\n", successCount, failCount)
	
	totalFailed := nfsFailCount + nsFailCount + ssFailCount + failCount
	if totalFailed > 0 {
		fmt.Printf("\n⚠ %d resource(s) failed to delete\n", totalFailed)
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
