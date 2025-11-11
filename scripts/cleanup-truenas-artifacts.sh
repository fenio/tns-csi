#!/bin/bash
# Cleanup script for TrueNAS test artifacts (datasets, NFS shares, NVMe-oF subsystems)
# This script connects to TrueNAS via WebSocket API and removes test-related artifacts

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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
echo -e "${BLUE}TrueNAS Artifact Cleanup${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo -e "${YELLOW}Host:${NC} ${TRUENAS_HOST}"
echo -e "${YELLOW}Pool:${NC} ${TRUENAS_POOL}"
echo ""

# Create a Go script to interact with TrueNAS API
cat > /tmp/truenas-cleanup.go <<'EOF'
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

	if host == "" || apiKey == "" || pool == "" {
		fmt.Println("Error: Missing required environment variables")
		os.Exit(1)
	}

	// Construct WebSocket URL
	url := fmt.Sprintf("wss://%s/api/current", host)
	
	fmt.Printf("Connecting to TrueNAS at %s...\n", url)
	
	client, err := tnsapi.NewClient(url, apiKey)
	if err != nil {
		fmt.Printf("Failed to create client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx := context.Background()

	// List all datasets in the pool (including both FILESYSTEM and VOLUME types)
	fmt.Println("\n=== Listing datasets (NFS filesystems and NVMe-oF ZVOLs) ===")
	var datasets []map[string]interface{}
	if err := client.Call(ctx, "pool.dataset.query", []interface{}{}, &datasets); err != nil {
		fmt.Printf("Failed to query datasets: %v\n", err)
		os.Exit(1)
	}

	testDatasets := []string{}
	for _, ds := range datasets {
		name, ok := ds["name"].(string)
		if !ok {
			continue
		}
		
		dsType, _ := ds["type"].(string) // FILESYSTEM or VOLUME
		
		// Only include datasets in the specified pool
		if !strings.HasPrefix(name, pool+"/") && name != pool {
			continue
		}
		
		// Only include CSI test datasets (containing 'pvc-' or 'test-csi')
		if strings.Contains(name, "pvc-") || strings.Contains(name, "test-csi") {
			testDatasets = append(testDatasets, name)
			if dsType == "VOLUME" {
				fmt.Printf("  Found ZVOL (NVMe-oF): %s\n", name)
			} else {
				fmt.Printf("  Found dataset (NFS): %s\n", name)
			}
		}
	}

	if len(testDatasets) == 0 {
		fmt.Println("\n✓ No test datasets (NFS or NVMe-oF) found - TrueNAS is clean!")
		return
	}

	fmt.Printf("\n=== Found %d test dataset(s) ===\n", len(testDatasets))

	// List NFS shares
	fmt.Println("\n=== Listing NFS shares ===")
	var shares []map[string]interface{}
	if err := client.Call(ctx, "sharing.nfs.query", []interface{}{}, &shares); err != nil {
		fmt.Printf("Failed to query NFS shares: %v\n", err)
		os.Exit(1)
	}

	testShares := []map[string]interface{}{}
	for _, share := range shares {
		path, ok := share["path"].(string)
		if !ok {
			continue
		}
		
		// Check if share path matches any test dataset
		for _, dsName := range testDatasets {
			if strings.Contains(path, dsName) {
				testShares = append(testShares, share)
				shareID := share["id"]
				fmt.Printf("  Found NFS share: %s (ID: %v)\n", path, shareID)
				break
			}
		}
	}

	// Delete NFS shares first
	if len(testShares) > 0 {
		fmt.Printf("\n=== Deleting %d NFS share(s) ===\n", len(testShares))
		for _, share := range testShares {
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
	}

	// NOTE: NVMe-oF subsystems are NOT deleted by this script.
	// Subsystems are pre-configured infrastructure that serve multiple volumes (namespaces).
	// Administrators manage subsystem lifecycle independently.
	// Only namespaces (automatically deleted when dataset is removed) are cleaned up.

	// Delete datasets
	fmt.Printf("\n=== Deleting %d dataset(s) ===\n", len(testDatasets))
	for _, dsName := range testDatasets {
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
		} else {
			fmt.Printf("    ✓ Deleted\n")
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println("\n✓ Cleanup complete!")
}
EOF

# Build and run the cleanup script
echo -e "${YELLOW}Building cleanup tool...${NC}"

# Store current directory
SCRIPT_DIR=$(pwd)
CLEANUP_DIR=$(mktemp -d)

# Copy the Go script to a temporary directory
cp /tmp/truenas-cleanup.go "$CLEANUP_DIR/"
cd "$CLEANUP_DIR"

# Initialize Go module with proper replace directive
go mod init cleanup
go mod edit -replace github.com/fenio/tns-csi="$SCRIPT_DIR"
go mod tidy

echo -e "${YELLOW}Running cleanup...${NC}"
echo ""

go run truenas-cleanup.go

# Cleanup
cd "$SCRIPT_DIR"
rm -rf "$CLEANUP_DIR"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Cleanup Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
