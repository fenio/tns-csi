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
			// - pvc-* datasets (volumes)
			// - test-csi* datasets (test artifacts)
			// - snapshot-* datasets (detached snapshots)
			// - csi-detached-snapshots folder and its contents
			if strings.Contains(name, "pvc-") ||
			   strings.Contains(name, "test-csi") ||
			   strings.Contains(name, "snapshot-") ||
			   strings.Contains(name, "csi-detached-snapshots") {
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
					ns.ID, device, ns.GetSubsystemID(), ns.NSID)
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

	// List iSCSI target-extent mappings and extents
	fmt.Println("\n=== Listing iSCSI extents ===")
	var iscsiExtents []map[string]interface{}
	targetExtents := []map[string]interface{}{}
	if err := client.Call(ctx, "iscsi.extent.query", []interface{}{}, &iscsiExtents); err != nil {
		fmt.Printf("Warning: Failed to query iSCSI extents: %v\n", err)
	} else {
		for _, extent := range iscsiExtents {
			disk, _ := extent["disk"].(string) // e.g., "zvol/storage/pvc-xxx"
			path, _ := extent["path"].(string) // alternative path field
			extentPath := disk
			if extentPath == "" {
				extentPath = path
			}

			shouldInclude := false
			if mode == "all" {
				if strings.Contains(extentPath, pool+"/") {
					shouldInclude = true
				}
			} else {
				if strings.Contains(extentPath, "pvc-") || strings.Contains(extentPath, "test-csi") {
					shouldInclude = true
				}
			}

			if shouldInclude {
				targetExtents = append(targetExtents, extent)
				extentID := extent["id"]
				extentName, _ := extent["name"].(string)
				fmt.Printf("  Found iSCSI extent: ID=%v, Name=%s, Path=%s\n", extentID, extentName, extentPath)
			}
		}
	}

	// List iSCSI targets
	fmt.Println("\n=== Listing iSCSI targets ===")
	var iscsiTargets []map[string]interface{}
	targetISCSITargets := []map[string]interface{}{}
	if err := client.Call(ctx, "iscsi.target.query", []interface{}{}, &iscsiTargets); err != nil {
		fmt.Printf("Warning: Failed to query iSCSI targets: %v\n", err)
	} else {
		for _, target := range iscsiTargets {
			name, _ := target["name"].(string)

			shouldInclude := false
			if mode == "all" {
				if strings.Contains(name, "pvc-") || strings.Contains(name, "test-") || strings.Contains(name, "csi-") {
					shouldInclude = true
				}
			} else {
				if strings.Contains(name, "pvc-") || strings.Contains(name, "test-csi") {
					shouldInclude = true
				}
			}

			if shouldInclude {
				targetISCSITargets = append(targetISCSITargets, target)
				targetID := target["id"]
				fmt.Printf("  Found iSCSI target: ID=%v, Name=%s\n", targetID, name)
			}
		}
	}

	if dryRun {
		fmt.Println("\n=== DRY RUN - No changes will be made ===")
		fmt.Printf("Would delete %d NFS share(s)\n", len(targetShares))
		fmt.Printf("Would delete %d NVMe-oF namespace(s)\n", len(targetNamespaces))
		fmt.Printf("Would delete %d NVMe-oF subsystem(s)\n", len(targetSubsystems))
		fmt.Printf("Would delete %d iSCSI extent(s)\n", len(targetExtents))
		fmt.Printf("Would delete %d iSCSI target(s)\n", len(targetISCSITargets))
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

	// Delete iSCSI target-extent mappings first, then extents, then targets
	// Query target-extent mappings
	var targetExtentMappings []map[string]interface{}
	if err := client.Call(ctx, "iscsi.targetextent.query", []interface{}{}, &targetExtentMappings); err != nil {
		fmt.Printf("Warning: Failed to query iSCSI target-extent mappings: %v\n", err)
	}

	// Build set of extent IDs and target IDs we want to delete
	extentIDsToDelete := make(map[int]bool)
	for _, extent := range targetExtents {
		if id, ok := extent["id"].(float64); ok {
			extentIDsToDelete[int(id)] = true
		}
	}
	targetIDsToDelete := make(map[int]bool)
	for _, target := range targetISCSITargets {
		if id, ok := target["id"].(float64); ok {
			targetIDsToDelete[int(id)] = true
		}
	}

	// Delete target-extent mappings for our targets/extents
	mappingSuccessCount := 0
	mappingFailCount := 0
	if len(targetExtentMappings) > 0 && (len(extentIDsToDelete) > 0 || len(targetIDsToDelete) > 0) {
		fmt.Println("\n=== Deleting iSCSI target-extent mappings ===")
		for _, mapping := range targetExtentMappings {
			mappingID := 0
			extentID := 0
			targetID := 0

			if id, ok := mapping["id"].(float64); ok {
				mappingID = int(id)
			}
			if id, ok := mapping["extent"].(float64); ok {
				extentID = int(id)
			}
			if id, ok := mapping["target"].(float64); ok {
				targetID = int(id)
			}

			// Delete if this mapping references an extent or target we want to delete
			if extentIDsToDelete[extentID] || targetIDsToDelete[targetID] {
				fmt.Printf("  Deleting mapping ID=%d (target=%d, extent=%d)...\n", mappingID, targetID, extentID)
				var result interface{}
				if err := client.Call(ctx, "iscsi.targetextent.delete", []interface{}{mappingID, true}, &result); err != nil {
					fmt.Printf("    ⚠ Failed: %v\n", err)
					mappingFailCount++
				} else {
					fmt.Printf("    ✓ Deleted\n")
					mappingSuccessCount++
				}
				time.Sleep(300 * time.Millisecond)
			}
		}
	}

	// Delete iSCSI extents
	extentSuccessCount := 0
	extentFailCount := 0
	if len(targetExtents) > 0 {
		fmt.Printf("\n=== Deleting %d iSCSI extent(s) ===\n", len(targetExtents))
		for _, extent := range targetExtents {
			extentID := 0
			if id, ok := extent["id"].(float64); ok {
				extentID = int(id)
			}
			extentName, _ := extent["name"].(string)
			fmt.Printf("  Deleting extent: ID=%d, Name=%s...\n", extentID, extentName)

			var result interface{}
			// Pass remove=true to also remove the underlying file/zvol if applicable
			if err := client.Call(ctx, "iscsi.extent.delete", []interface{}{extentID, true, true}, &result); err != nil {
				fmt.Printf("    ⚠ Failed: %v\n", err)
				extentFailCount++
			} else {
				fmt.Printf("    ✓ Deleted\n")
				extentSuccessCount++
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No iSCSI extents to delete ===")
	}

	// Delete iSCSI targets
	iscsiTargetSuccessCount := 0
	iscsiTargetFailCount := 0
	if len(targetISCSITargets) > 0 {
		fmt.Printf("\n=== Deleting %d iSCSI target(s) ===\n", len(targetISCSITargets))
		for _, target := range targetISCSITargets {
			targetID := 0
			if id, ok := target["id"].(float64); ok {
				targetID = int(id)
			}
			targetName, _ := target["name"].(string)
			fmt.Printf("  Deleting target: ID=%d, Name=%s...\n", targetID, targetName)

			var result interface{}
			if err := client.Call(ctx, "iscsi.target.delete", []interface{}{targetID, true}, &result); err != nil {
				fmt.Printf("    ⚠ Failed: %v\n", err)
				iscsiTargetFailCount++
			} else {
				fmt.Printf("    ✓ Deleted\n")
				iscsiTargetSuccessCount++
			}
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		fmt.Println("\n=== No iSCSI targets to delete ===")
	}

	// Delete datasets - need to sort by clone dependencies (delete clones before origins)
	// First, get origin information for all datasets
	fmt.Println("\n=== Analyzing dataset dependencies ===")
	datasetOrigins := make(map[string]string) // dataset -> origin
	for _, ds := range datasets {
		name, ok := ds["name"].(string)
		if !ok {
			continue
		}
		// Check if this dataset has an origin (is a clone)
		if origin, ok := ds["origin"].(map[string]interface{}); ok {
			if originValue, ok := origin["value"].(string); ok && originValue != "" {
				// Origin format is "pool/dataset@snapshot", extract just the dataset part
				originDS := originValue
				if atIdx := strings.Index(originValue, "@"); atIdx > 0 {
					originDS = originValue[:atIdx]
				}
				datasetOrigins[name] = originDS
				fmt.Printf("  %s is a clone of %s\n", name, originDS)
			}
		}
	}

	// Sort datasets: clones before their origins
	// Use a simple approach: datasets with origins (clones) first, then others
	// For nested clones, sort by depth (more origins = delete first)
	sortedDatasets := make([]string, 0, len(targetDatasets))

	// Helper to count clone depth
	getCloneDepth := func(ds string) int {
		depth := 0
		current := ds
		visited := make(map[string]bool)
		for {
			origin, hasOrigin := datasetOrigins[current]
			if !hasOrigin || visited[current] {
				break
			}
			visited[current] = true
			depth++
			current = origin
		}
		return depth
	}

	// Create a slice with depths for sorting
	type datasetWithDepth struct {
		name  string
		depth int
	}
	dsWithDepths := make([]datasetWithDepth, 0, len(targetDatasets))
	for _, ds := range targetDatasets {
		dsWithDepths = append(dsWithDepths, datasetWithDepth{name: ds, depth: getCloneDepth(ds)})
	}

	// Sort by depth descending (deepest clones first)
	for i := 0; i < len(dsWithDepths); i++ {
		for j := i + 1; j < len(dsWithDepths); j++ {
			if dsWithDepths[j].depth > dsWithDepths[i].depth {
				dsWithDepths[i], dsWithDepths[j] = dsWithDepths[j], dsWithDepths[i]
			}
		}
	}

	for _, dwd := range dsWithDepths {
		sortedDatasets = append(sortedDatasets, dwd.name)
	}

	if len(datasetOrigins) > 0 {
		fmt.Printf("  Sorted %d dataset(s) by clone dependency\n", len(sortedDatasets))
	}

	// Track if we've already tried restarting NFS
	nfsRestarted := false

	// Helper function to restart NFS service to release stale mounts
	restartNFS := func() error {
		if nfsRestarted {
			return fmt.Errorf("NFS already restarted this session")
		}
		fmt.Println("    Restarting NFS service to release stale mounts...")

		// Use service.stop then service.start (service.control doesn't have a restart action)
		var result interface{}

		// Stop NFS
		stopParams := []interface{}{"nfs", map[string]interface{}{"ha_propagate": false}}
		if err := client.Call(ctx, "service.stop", stopParams, &result); err != nil {
			fmt.Printf("    Warning: service.stop failed: %v\n", err)
		} else {
			fmt.Println("    NFS service stopped")
		}

		time.Sleep(2 * time.Second)

		// Start NFS
		startParams := []interface{}{"nfs", map[string]interface{}{"ha_propagate": false}}
		if err := client.Call(ctx, "service.start", startParams, &result); err != nil {
			return fmt.Errorf("service.start failed: %v", err)
		}
		fmt.Println("    NFS service started")

		nfsRestarted = true
		time.Sleep(3 * time.Second) // Give NFS time to fully start
		return nil
	}

	// Delete datasets with NFS restart for busy datasets
	successCount := 0
	failCount := 0
	if len(sortedDatasets) > 0 {
		fmt.Printf("\n=== Deleting %d dataset(s) ===\n", len(sortedDatasets))
		for _, dsName := range sortedDatasets {
			fmt.Printf("  Deleting dataset: %s...\n", dsName)

			var result interface{}
			deleted := false

			// First attempt with recursive and force
			params := []interface{}{
				dsName,
				map[string]interface{}{
					"recursive": true,
					"force":     true,
				},
			}

			if err := client.Call(ctx, "pool.dataset.delete", params, &result); err != nil {
				errStr := err.Error()

				// Check if it's a "busy" error - try restarting NFS to release stale mounts
				if strings.Contains(errStr, "EBUSY") || strings.Contains(errStr, "busy") || strings.Contains(errStr, "cannot unmount") {
					fmt.Printf("    Dataset is busy...\n")

					// Try restarting NFS service to release stale mounts
					if nfsErr := restartNFS(); nfsErr != nil {
						fmt.Printf("    Warning: %v\n", nfsErr)
					}

					time.Sleep(2 * time.Second)

					// Retry delete after NFS restart
					if err2 := client.Call(ctx, "pool.dataset.delete", params, &result); err2 != nil {
						errStr2 := err2.Error()

						// If still busy, wait longer and try once more
						if strings.Contains(errStr2, "EBUSY") || strings.Contains(errStr2, "busy") {
							fmt.Printf("    Still busy, waiting 5s and retrying...\n")
							time.Sleep(5 * time.Second)

							// Final attempt
							if err3 := client.Call(ctx, "pool.dataset.delete", params, &result); err3 != nil {
								fmt.Printf("    ⚠ Failed after NFS restart: %v\n", err3)
								fmt.Printf("    → Manual cleanup required: zfs destroy -f %s\n", dsName)
								failCount++
							} else {
								fmt.Printf("    ✓ Deleted on final retry\n")
								successCount++
								deleted = true
							}
						} else if strings.Contains(errStr2, "does not exist") || strings.Contains(errStr2, "ENOENT") {
							fmt.Printf("    ✓ Already deleted\n")
							successCount++
							deleted = true
						} else {
							fmt.Printf("    ⚠ Still failed after NFS restart: %v\n", err2)
							fmt.Printf("    → Manual cleanup required: zfs destroy -f %s\n", dsName)
							failCount++
						}
					} else {
						fmt.Printf("    ✓ Deleted after NFS restart\n")
						successCount++
						deleted = true
					}
				} else if strings.Contains(errStr, "does not exist") || strings.Contains(errStr, "ENOENT") {
					// Dataset already gone, that's fine
					fmt.Printf("    ✓ Already deleted\n")
					successCount++
					deleted = true
				} else if strings.Contains(errStr, "dependent clones") {
					// Clone dependency issue - the clone should have been deleted first
					// but if it failed, we can't delete the parent
					fmt.Printf("    ⚠ Has dependent clones (clone deletion may have failed): %v\n", err)
					failCount++
				} else {
					fmt.Printf("    ⚠ Failed to delete dataset: %v\n", err)
					failCount++
				}
			} else {
				fmt.Printf("    ✓ Deleted\n")
				successCount++
				deleted = true
			}

			if deleted {
				time.Sleep(500 * time.Millisecond)
			} else {
				time.Sleep(1 * time.Second) // Longer delay after failures
			}
		}
	} else {
		fmt.Println("\n=== No datasets to delete ===")
	}

	fmt.Println("\n=== Summary ===")
	fmt.Printf("NFS shares:            %d deleted, %d failed\n", nfsSuccessCount, nfsFailCount)
	fmt.Printf("NVMe-oF namespaces:    %d deleted, %d failed\n", nsSuccessCount, nsFailCount)
	fmt.Printf("NVMe-oF port bindings: %d removed\n", portBindingCount)
	fmt.Printf("NVMe-oF subsystems:    %d deleted, %d failed\n", ssSuccessCount, ssFailCount)
	fmt.Printf("iSCSI mappings:        %d deleted, %d failed\n", mappingSuccessCount, mappingFailCount)
	fmt.Printf("iSCSI extents:         %d deleted, %d failed\n", extentSuccessCount, extentFailCount)
	fmt.Printf("iSCSI targets:         %d deleted, %d failed\n", iscsiTargetSuccessCount, iscsiTargetFailCount)
	fmt.Printf("Datasets:              %d deleted, %d failed\n", successCount, failCount)

	totalFailed := nfsFailCount + nsFailCount + ssFailCount + mappingFailCount + extentFailCount + iscsiTargetFailCount + failCount
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
