package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// cloneParameters holds validated parameters for snapshot cloning.
type cloneParameters struct {
	datasetProperties map[string]string
	pool              string
	parentDataset     string
	newVolumeName     string
	newDatasetName    string
}

// cloneInfo holds metadata about how a clone was created.
// This is passed to setup functions to record the clone mode in ZFS properties.
type cloneInfo struct {
	// Mode is the clone mode: "cow", "promoted", or "detached"
	Mode string
	// OriginSnapshot is the ZFS snapshot the clone was created from (for COW clones)
	OriginSnapshot string
	// SnapshotID is the CSI snapshot ID used as the source
	SnapshotID string
}

// createVolumeFromSnapshot creates a new volume from a snapshot by cloning.
func (s *ControllerService) createVolumeFromSnapshot(ctx context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("=== createVolumeFromSnapshot CALLED === Volume: %s, SnapshotID: %s", req.GetName(), snapshotID)
	klog.V(4).Infof("Full request: %+v", req)

	// Decode snapshot metadata
	snapshotMeta, decodeErr := decodeSnapshotID(snapshotID)
	if decodeErr != nil {
		klog.Warningf("Failed to decode snapshot ID %s: %v. Treating as not found.", snapshotID, decodeErr)
		return nil, status.Errorf(codes.NotFound, "Snapshot not found: %s", snapshotID)
	}
	klog.Infof("Decoded snapshot ID: SnapshotName=%s, SourceVolume=%s, Protocol=%s, Detached=%v",
		snapshotMeta.SnapshotName, snapshotMeta.SourceVolume, snapshotMeta.Protocol, snapshotMeta.Detached)

	// Resolve the full ZFS snapshot name and dataset info if using compact format
	if resolveErr := s.resolveSnapshotMetadata(ctx, snapshotMeta); resolveErr != nil {
		klog.Warningf("Failed to resolve snapshot metadata: %v. Treating as not found.", resolveErr)
		return nil, status.Errorf(codes.NotFound, "Snapshot not found: %s", snapshotID)
	}
	klog.Infof("Resolved snapshot metadata: DatasetName=%s, Protocol=%s, Detached=%v",
		snapshotMeta.DatasetName, snapshotMeta.Protocol, snapshotMeta.Detached)

	// Validate and extract clone parameters
	cloneParams, validateErr := s.validateCloneParameters(req, snapshotMeta)
	if validateErr != nil {
		return nil, validateErr
	}

	// Get request parameters for later use
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	// Determine clone mode from StorageClass parameters:
	// - detachedVolumesFromSnapshots=true: Use send/receive for truly independent copy
	// - promotedVolumesFromSnapshots=true: Use clone+promote (reversed dependency)
	// - default: Standard COW clone (clone depends on snapshot, clone can be deleted freely)
	//
	// WARNING: Promoted mode reverses the ZFS dependency — the SOURCE becomes dependent on
	// the CLONE. This means you cannot delete the clone while the source exists. Only use
	// promoted mode when you intend to delete the source volume before the restored clone.
	// For most use cases (including VolSync), use detachedVolumesFromSnapshots=true instead.
	detachedMode := params[DetachedVolumesFromSnapshotsParam] == VolumeContextValueTrue
	promotedMode := params[PromotedVolumesFromSnapshotsParam] == VolumeContextValueTrue

	if detachedMode && promotedMode {
		klog.Warningf("Both detachedVolumesFromSnapshots and promotedVolumesFromSnapshots are set; using detached mode")
		promotedMode = false
	}

	// Clone/restore the snapshot based on source type and clone mode:
	//
	// Source types:
	// - Detached snapshot (stored as dataset): Create temp snapshot, then clone from it
	// - Regular ZFS snapshot: Clone directly from it
	//
	// Clone modes (applies to both source types):
	// 1. detachedVolumesFromSnapshots=true -> send/receive (truly independent, slow)
	//    Note: Not supported for detached snapshot sources; falls back to COW
	// 2. promotedVolumesFromSnapshots=true -> clone+promote (reversed dependency)
	// 3. default -> COW clone (clone depends on snapshot, can be deleted freely)

	type cloneMode int
	const (
		cloneModeDetachedSnapshotRestore cloneMode = iota
		cloneModeDetached
		cloneModePromoted
		cloneModeCOW
	)

	// promoteDetachedRestore controls whether to promote after restoring from a detached snapshot.
	promoteDetachedRestore := false

	var mode cloneMode
	switch {
	case snapshotMeta.Detached:
		// Source is a detached snapshot (stored as dataset, not a ZFS snapshot).
		// Must use executeDetachedSnapshotRestore (creates temp snapshot, then clones).
		// send/receive mode is not supported from detached snapshot sources.
		mode = cloneModeDetachedSnapshotRestore
		if detachedMode {
			klog.Warningf("detachedVolumesFromSnapshots is not supported for detached snapshot sources; using COW mode")
		}
		// Only promote if explicitly requested via promotedVolumesFromSnapshots.
		promoteDetachedRestore = promotedMode
	case detachedMode:
		mode = cloneModeDetached
	case promotedMode:
		mode = cloneModePromoted
	default:
		mode = cloneModeCOW
	}

	var clonedDataset *tnsapi.Dataset
	var cloneErr error

	switch mode {
	case cloneModeDetachedSnapshotRestore:
		// Source is a detached snapshot (stored as a dataset, not a ZFS snapshot)
		// Create temp snapshot on the dataset, clone from it, optionally promote
		klog.Infof("Restoring volume %s from detached snapshot dataset %s (promote=%v)", req.GetName(), snapshotMeta.DatasetName, promoteDetachedRestore)
		clonedDataset, cloneErr = s.executeDetachedSnapshotRestore(ctx, snapshotMeta, cloneParams, promoteDetachedRestore)
	case cloneModeDetached:
		// Truly independent copy via send/receive
		klog.Infof("Creating detached (send/receive) volume %s from snapshot (truly independent)", req.GetName())
		clonedDataset, cloneErr = s.executeDetachedVolumeClone(ctx, snapshotMeta, cloneParams)
	case cloneModePromoted:
		// Clone+promote (reversed dependency, allows snapshot deletion)
		klog.Infof("Creating promoted clone for volume %s from snapshot (reversed dependency)", req.GetName())
		clonedDataset, cloneErr = s.executePromotedSnapshotClone(ctx, snapshotMeta, cloneParams)
	case cloneModeCOW:
		// Explicit COW clone (clone depends on snapshot)
		klog.Infof("Creating COW clone for volume %s from snapshot (normal dependency)", req.GetName())
		clonedDataset, cloneErr = s.executeSnapshotClone(ctx, snapshotMeta, cloneParams)
	}
	if cloneErr != nil {
		return nil, cloneErr
	}
	klog.Infof("Clone operation succeeded: dataset=%s, type=%s, mountpoint=%s",
		clonedDataset.Name, clonedDataset.Type, clonedDataset.Mountpoint)

	// Build clone info for property tracking
	cloneInfoData := cloneInfo{
		SnapshotID: snapshotID,
	}
	switch mode {
	case cloneModeDetachedSnapshotRestore:
		if promoteDetachedRestore {
			// Promoted restore: clone was promoted, no COW dependency
			cloneInfoData.Mode = tnsapi.CloneModePromoted
		} else {
			// COW restore: clone depends on temp snapshot
			cloneInfoData.Mode = tnsapi.CloneModeCOW
			cloneInfoData.OriginSnapshot = snapshotMeta.DatasetName + "@csi-restore-for-" + req.GetName()
		}
	case cloneModeDetached:
		cloneInfoData.Mode = tnsapi.CloneModeDetached
		// No origin for detached clones (truly independent)
	case cloneModePromoted:
		cloneInfoData.Mode = tnsapi.CloneModePromoted
		// Origin was the snapshot, but after promotion the dependency is reversed
	case cloneModeCOW:
		cloneInfoData.Mode = tnsapi.CloneModeCOW
		cloneInfoData.OriginSnapshot = snapshotMeta.SnapshotName
	}

	// Wait for ZFS metadata sync for NVMe-oF volumes
	s.waitForZFSSyncIfNVMeOF(snapshotMeta.Protocol)

	// Get server and subsystemNQN parameters
	server, subsystemNQN, err := s.getVolumeParametersForSnapshot(ctx, params, snapshotMeta, clonedDataset)
	if err != nil {
		klog.Errorf("Failed to get volume parameters for snapshot: %v", err)
		return nil, err
	}
	klog.Infof("Got volume parameters: server=%s, subsystemNQN=%s, protocol=%s", server, subsystemNQN, snapshotMeta.Protocol)

	// Route to protocol-specific volume setup
	klog.Infof("Routing to protocol-specific setup: protocol=%s, cloneMode=%s", snapshotMeta.Protocol, cloneInfoData.Mode)
	return s.setupVolumeFromClone(ctx, req, clonedDataset, snapshotMeta.Protocol, server, subsystemNQN, &cloneInfoData)
}

// resolveSnapshotMetadata resolves missing metadata fields for compact format snapshots.
// For legacy format, the metadata is already complete.
// For compact format, we need to look up the full ZFS snapshot name and dataset info.
// For detached snapshots (stored as datasets), we use property-based lookup.
func (s *ControllerService) resolveSnapshotMetadata(ctx context.Context, meta *SnapshotMetadata) error {
	// If SnapshotName already contains "@", it's the full ZFS path (legacy format)
	// and DatasetName should also be populated
	if strings.Contains(meta.SnapshotName, "@") && meta.DatasetName != "" {
		return nil
	}

	// Detached snapshots are stored as datasets, not ZFS snapshots
	// Use property-based lookup to find the dataset
	if meta.Detached {
		return s.resolveDetachedSnapshotMetadata(ctx, meta)
	}

	// Regular snapshot: Compact format: need to resolve full paths
	zfsSnapshotName, err := s.resolveZFSSnapshotName(ctx, meta)
	if err != nil {
		return err
	}

	// Update metadata with resolved values
	meta.SnapshotName = zfsSnapshotName

	// Extract dataset name from full ZFS snapshot name (format: dataset@snapname)
	if idx := strings.LastIndex(zfsSnapshotName, "@"); idx != -1 {
		meta.DatasetName = zfsSnapshotName[:idx]
	}

	klog.V(4).Infof("Resolved snapshot metadata: SnapshotName=%s, DatasetName=%s",
		meta.SnapshotName, meta.DatasetName)

	return nil
}

// resolveDetachedSnapshotMetadata resolves metadata for detached snapshots using property-based lookup.
// Detached snapshots are stored as datasets with tns-csi:detached_snapshot=true property.
func (s *ControllerService) resolveDetachedSnapshotMetadata(ctx context.Context, meta *SnapshotMetadata) error {
	klog.Infof("=== resolveDetachedSnapshotMetadata CALLED === snapshot_id: %q, SourceVolume: %q, Protocol: %s",
		meta.SnapshotName, meta.SourceVolume, meta.Protocol)

	// Use property-based lookup to find the detached snapshot dataset
	// Search globally (empty prefix) to find detached snapshots across all pools
	resolvedMeta, err := s.lookupSnapshotByCSIName(ctx, "", meta.SnapshotName)
	if err != nil {
		klog.Errorf("Property-based lookup failed for detached snapshot %s: %v", meta.SnapshotName, err)
		return fmt.Errorf("failed to lookup detached snapshot %s: %w", meta.SnapshotName, err)
	}

	if resolvedMeta == nil {
		klog.Errorf("Detached snapshot dataset not found for snapshot_id: %s (property tns-csi:snapshot_id not found on any dataset)", meta.SnapshotName)
		return fmt.Errorf("%w: %s", ErrDetachedSnapshotNotFound, meta.SnapshotName)
	}

	// Update metadata with resolved values
	meta.DatasetName = resolvedMeta.DatasetName
	if resolvedMeta.Protocol != "" {
		meta.Protocol = resolvedMeta.Protocol
	}
	if resolvedMeta.SourceVolume != "" {
		meta.SourceVolume = resolvedMeta.SourceVolume
	}

	klog.V(4).Infof("Resolved detached snapshot metadata: SnapshotName=%s, DatasetName=%s, Protocol=%s",
		meta.SnapshotName, meta.DatasetName, meta.Protocol)

	return nil
}

// validateCloneParameters validates and extracts parameters needed for cloning.
func (s *ControllerService) validateCloneParameters(req *csi.CreateVolumeRequest, snapshotMeta *SnapshotMetadata) (*cloneParameters, error) {
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	// Try to get pool from parameters (StorageClass)
	pool := params["pool"]
	parentDataset := params["parentDataset"]

	// Validate snapshot dataset name
	if snapshotMeta.DatasetName == "" {
		return nil, status.Error(codes.Internal, "Snapshot dataset name is empty")
	}

	// If pool is not provided in parameters, infer it from the snapshot's source dataset
	// This is critical for snapshot restoration to work properly
	if pool == "" {
		// Extract pool from snapshot's dataset name
		// DatasetName format: "pool/dataset" or "pool/parent/dataset"
		parts := strings.Split(snapshotMeta.DatasetName, "/")
		if len(parts) > 0 && parts[0] != "" {
			pool = parts[0]
			klog.V(4).Infof("Inferred pool %q from snapshot dataset %q", pool, snapshotMeta.DatasetName)
		} else {
			return nil, status.Errorf(codes.Internal, "Failed to extract pool from snapshot dataset: %s", snapshotMeta.DatasetName)
		}
	}

	// If parentDataset is not provided, infer from snapshot's dataset path or use pool
	if parentDataset == "" {
		// For detached snapshots, use pool directly since the snapshot is stored in a
		// separate location (pool/csi-detached-snapshots/). We don't want to create
		// restored volumes in the detached snapshots folder.
		if snapshotMeta.Detached {
			parentDataset = pool
			klog.V(4).Infof("Using pool %q as parentDataset for detached snapshot restore", pool)
		} else {
			// For regular snapshots, infer from snapshot's dataset path
			parts := strings.Split(snapshotMeta.DatasetName, "/")
			if len(parts) > 1 {
				// Use the same parent dataset structure as the source volume
				// For dataset "pool/parent/volume", use "pool/parent"
				parentDataset = strings.Join(parts[:len(parts)-1], "/")
				klog.V(4).Infof("Inferred parentDataset %q from snapshot dataset %q", parentDataset, snapshotMeta.DatasetName)
			} else {
				// Just use the pool as parent
				parentDataset = pool
				klog.V(4).Infof("Using pool %q as parentDataset", pool)
			}
		}
	}

	newVolumeName := req.GetName()
	newDatasetName := fmt.Sprintf("%s/%s", parentDataset, newVolumeName)

	klog.Infof("Cloning snapshot %s (dataset: %s) to new volume %s (new dataset: %s)",
		snapshotMeta.SnapshotName, snapshotMeta.DatasetName, newVolumeName, newDatasetName)

	cp := &cloneParameters{
		pool:           pool,
		parentDataset:  parentDataset,
		newVolumeName:  newVolumeName,
		newDatasetName: newDatasetName,
	}

	// SMB datasets need NFSv4 ACLs (set by share_type: "SMB" on new datasets).
	// ZFS clones inherit acltype from the parent in the hierarchy, NOT the origin.
	// Explicitly set acltype/aclmode so the clone matches the source dataset.
	if snapshotMeta.Protocol == ProtocolSMB {
		cp.datasetProperties = map[string]string{
			"acltype": "nfsv4",
			"aclmode": "restricted",
		}
	}

	return cp, nil
}

// executeSnapshotClone performs the actual snapshot clone operation.
func (s *ControllerService) executeSnapshotClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Cloning snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot:          snapshotMeta.SnapshotName,
		Dataset:           params.newDatasetName,
		DatasetProperties: params.datasetProperties,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneParams)
	if err != nil {
		klog.Errorf("Failed to clone snapshot: %v. Checking if dataset was created...", err)
		s.cleanupPartialClone(ctx, params.newDatasetName)
		return nil, status.Errorf(codes.Internal, "Failed to clone snapshot: %v", err)
	}

	klog.Infof("Successfully cloned snapshot to dataset: %s", clonedDataset.Name)
	return clonedDataset, nil
}

// executePromotedSnapshotClone creates a clone and promotes it, reversing the dependency.
// After promotion:
// - The source volume/snapshot depends on the clone (cannot delete clone while source exists)
// - The original snapshot can be deleted (useful for snapshot rotation)
//
// This is a trade-off:
// - Pro: Allows deleting the original snapshot
// - Con: Clone cannot be deleted while source volume exists
//
// Note: For restore-from-snapshot, the dependency is reversed such that the SNAPSHOT
// depends on the clone. This means deleting the clone will be blocked while the snapshot exists.
func (s *ControllerService) executePromotedSnapshotClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Creating promoted clone from snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	// Step 1: Clone the snapshot (same as regular clone)
	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot:          snapshotMeta.SnapshotName,
		Dataset:           params.newDatasetName,
		DatasetProperties: params.datasetProperties,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneParams)
	if err != nil {
		klog.Errorf("Failed to clone snapshot for promotion: %v", err)
		s.cleanupPartialClone(ctx, params.newDatasetName)
		return nil, status.Errorf(codes.Internal, "Failed to clone snapshot: %v", err)
	}

	klog.V(4).Infof("Clone created: %s, now promoting to reverse dependency", clonedDataset.Name)

	// Step 2: Promote the clone to reverse the dependency
	// After promotion: snapshot depends on clone (clone becomes the origin)
	if err := s.apiClient.PromoteDataset(ctx, params.newDatasetName); err != nil {
		klog.Errorf("Failed to promote clone %s: %v. Cleaning up.", params.newDatasetName, err)
		// Cleanup the clone since we couldn't complete the operation
		if delErr := s.apiClient.DeleteDataset(ctx, params.newDatasetName); delErr != nil {
			klog.Errorf("Failed to cleanup clone after promotion failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to promote clone: %v", err)
	}

	klog.Infof("Successfully created promoted clone: %s (dependency reversed, snapshot can be deleted)", clonedDataset.Name)
	return clonedDataset, nil
}

// executeDetachedVolumeClone creates a truly independent volume via zfs send/receive.
// The resulting volume has NO dependency on the source snapshot.
// This is slower than clone+promote but provides complete independence:
// - Both source and clone can be deleted in any order
// - No shared blocks (full data copy)
//
// This uses the same mechanism as detached snapshots (one-time replication).
func (s *ControllerService) executeDetachedVolumeClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Creating detached (send/receive) volume from snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	// Step 1: Run one-time replication (zfs send/receive) to create independent copy
	// We use the snapshot directly as the source, not the parent dataset
	sourceDataset := snapshotMeta.DatasetName
	snapshotNameOnly := snapshotMeta.SnapshotName
	if idx := strings.LastIndex(snapshotMeta.SnapshotName, "@"); idx != -1 {
		snapshotNameOnly = snapshotMeta.SnapshotName[idx+1:]
	}

	klog.V(4).Infof("Running one-time replication from %s (snapshot: %s) to %s",
		sourceDataset, snapshotNameOnly, params.newDatasetName)

	replicationParams := tnsapi.ReplicationRunOnetimeParams{
		Direction:               "PUSH",
		Transport:               "LOCAL",
		SourceDatasets:          []string{sourceDataset},
		TargetDataset:           params.newDatasetName,
		Recursive:               false,
		Properties:              true,
		PropertiesExclude:       []string{"mountpoint", "sharenfs", "sharesmb", tnsapi.PropertyCSIVolumeName},
		Replicate:               false,
		Encryption:              false,
		NameRegex:               &snapshotNameOnly, // Only send the specific snapshot
		NamingSchema:            []string{},
		AlsoIncludeNamingSchema: []string{},
		RetentionPolicy:         "NONE",
		Readonly:                "IGNORE",
		AllowFromScratch:        true,
	}

	err := s.apiClient.RunOnetimeReplicationAndWait(ctx, replicationParams, ReplicationPollInterval)
	if err != nil {
		klog.Errorf("Detached volume clone replication failed: %v. Attempting cleanup of %s", err, params.newDatasetName)
		if delErr := s.apiClient.DeleteDataset(ctx, params.newDatasetName); delErr != nil {
			klog.Warningf("Failed to cleanup partial detached clone dataset: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create detached volume clone via replication: %v", err)
	}

	klog.V(4).Infof("Replication completed for detached volume clone: %s", params.newDatasetName)

	// Step 2: Promote to ensure complete independence
	// LOCAL replication may create clone relationships for efficiency
	klog.V(4).Infof("Promoting detached volume clone %s to ensure independence", params.newDatasetName)
	if promoteErr := s.apiClient.PromoteDataset(ctx, params.newDatasetName); promoteErr != nil {
		klog.Warningf("PromoteDataset(%s) failed: %v (continuing, may still work)", params.newDatasetName, promoteErr)
	} else {
		klog.V(4).Infof("Successfully promoted detached volume clone: %s", params.newDatasetName)
	}

	// Step 3: Clean up the replicated snapshot from the target dataset
	targetSnapshot := fmt.Sprintf("%s@%s", params.newDatasetName, snapshotNameOnly)
	klog.V(4).Infof("Cleaning up replicated snapshot %s", targetSnapshot)
	if delErr := s.apiClient.DeleteSnapshot(ctx, targetSnapshot); delErr != nil {
		klog.Warningf("Failed to delete replicated snapshot %s: %v (non-fatal)", targetSnapshot, delErr)
	}

	// Step 4: Query the dataset to get its full info
	clonedDataset, err := s.apiClient.Dataset(ctx, params.newDatasetName)
	if err != nil {
		klog.Errorf("Failed to query detached clone dataset %s: %v", params.newDatasetName, err)
		return nil, status.Errorf(codes.Internal, "Failed to query detached clone dataset: %v", err)
	}

	klog.Infof("Successfully created detached (send/receive) volume: %s (truly independent, no dependencies)", clonedDataset.Name)
	return clonedDataset, nil
}

// executeDetachedSnapshotRestore restores a volume from a detached snapshot.
// Detached snapshots are stored as datasets (not ZFS snapshots), so we need to
// create a ZFS snapshot of it first, then clone from that snapshot.
//
// When promote is true (default), the clone is promoted after creation. This breaks
// the COW dependency chain, allowing both the restored volume and the detached
// snapshot to be managed independently. This is essential for VolSync and other
// backup tools that create and delete restored volumes as part of their workflow.
//
// When promote is false (cowVolumesFromSnapshots=true), the clone maintains a COW
// dependency on the temp snapshot. The restored volume can be deleted freely (it's
// the dependent), but the detached snapshot cannot be deleted while clones exist.
func (s *ControllerService) executeDetachedSnapshotRestore(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters, promote bool) (*tnsapi.Dataset, error) {
	klog.Infof("Restoring volume from detached snapshot dataset %s to %s (promote=%v)", snapshotMeta.DatasetName, params.newDatasetName, promote)

	// Step 1: Create a temporary ZFS snapshot of the detached snapshot dataset
	tempSnapshotName := "csi-restore-for-" + params.newVolumeName
	tempSnapshotFullName := snapshotMeta.DatasetName + "@" + tempSnapshotName

	klog.V(4).Infof("Creating snapshot %s for restore operation", tempSnapshotFullName)

	// Check if snapshot already exists (idempotency for retried operations)
	existingSnapshots, queryErr := s.apiClient.QuerySnapshots(ctx, []interface{}{
		[]interface{}{"dataset", "=", snapshotMeta.DatasetName},
	})
	if queryErr != nil {
		klog.V(4).Infof("Failed to query existing snapshots (will attempt to create): %v", queryErr)
	}
	snapshotExists := false
	for _, snap := range existingSnapshots {
		if snap.Name == tempSnapshotFullName {
			klog.Infof("Snapshot %s already exists, reusing for restore", tempSnapshotFullName)
			snapshotExists = true
			break
		}
	}

	if !snapshotExists {
		_, err := s.apiClient.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{
			Dataset:   snapshotMeta.DatasetName,
			Name:      tempSnapshotName,
			Recursive: false,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create snapshot of detached snapshot dataset: %v", err)
		}
	}

	// Step 2: Clone the snapshot to create the new volume
	klog.V(4).Infof("Cloning snapshot %s to %s", tempSnapshotFullName, params.newDatasetName)

	cloneSnapshotParams := tnsapi.CloneSnapshotParams{
		Snapshot:          tempSnapshotFullName,
		Dataset:           params.newDatasetName,
		DatasetProperties: params.datasetProperties,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneSnapshotParams)
	if err != nil {
		klog.Errorf("Failed to clone snapshot: %v", err)
		// Don't delete the temp snapshot - it might be used by other restores
		// or might be needed for a retry
		return nil, status.Errorf(codes.Internal, "Failed to clone detached snapshot: %v", err)
	}

	// Step 3: Optionally promote the clone to break COW dependency
	if promote {
		klog.V(4).Infof("Promoting clone %s to break COW dependency with detached snapshot", params.newDatasetName)
		if promoteErr := s.apiClient.PromoteDataset(ctx, params.newDatasetName); promoteErr != nil {
			klog.Errorf("Failed to promote clone %s: %v. Cleaning up.", params.newDatasetName, promoteErr)
			if delErr := s.apiClient.DeleteDataset(ctx, params.newDatasetName); delErr != nil {
				klog.Errorf("Failed to cleanup clone after promotion failure: %v", delErr)
			}
			return nil, status.Errorf(codes.Internal, "Failed to promote clone from detached snapshot: %v", promoteErr)
		}

		// After promotion, the temp snapshot has moved from the detached snapshot dataset
		// to the promoted clone. Clean it up since it's no longer needed.
		promotedTempSnapshot := params.newDatasetName + "@" + tempSnapshotName
		klog.V(4).Infof("Deleting temp snapshot %s (moved to promoted clone after promotion)", promotedTempSnapshot)
		if delErr := s.apiClient.DeleteSnapshot(ctx, promotedTempSnapshot); delErr != nil {
			klog.Warningf("Failed to delete temp snapshot %s after promotion: %v (non-fatal)", promotedTempSnapshot, delErr)
		}

		klog.Infof("Successfully restored and promoted volume from detached snapshot: %s -> %s (no COW dependency)",
			snapshotMeta.DatasetName, clonedDataset.Name)
	} else {
		klog.Infof("Successfully restored volume from detached snapshot: %s -> %s (COW clone depends on %s)",
			snapshotMeta.DatasetName, clonedDataset.Name, tempSnapshotFullName)
	}

	return clonedDataset, nil
}

// cleanupPartialClone attempts to clean up a partially created cloned dataset.
func (s *ControllerService) cleanupPartialClone(ctx context.Context, datasetName string) {
	if delErr := s.apiClient.DeleteDataset(ctx, datasetName); delErr != nil {
		if !isNotFoundError(delErr) {
			klog.Errorf("Failed to cleanup potentially partially-created dataset %s: %v", datasetName, delErr)
		}
	} else {
		klog.Infof("Cleaned up partially-created dataset: %s", datasetName)
	}
}

// waitForZFSSyncIfNVMeOF waits for ZFS metadata to sync for NVMe-oF volumes.
func (s *ControllerService) waitForZFSSyncIfNVMeOF(protocol string) {
	if protocol != ProtocolNVMeOF {
		return
	}
	const zfsSyncDelay = 5 * time.Second
	klog.Infof("Waiting %v for ZFS metadata to sync before creating NVMe-oF namespace", zfsSyncDelay)
	time.Sleep(zfsSyncDelay)
	klog.V(4).Infof("ZFS sync delay complete, proceeding with NVMe-oF namespace creation")
}

// setupVolumeFromClone routes to the appropriate protocol-specific volume setup.
func (s *ControllerService) setupVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, clonedDataset *tnsapi.Dataset, protocol, server, subsystemNQN string, info *cloneInfo) (*csi.CreateVolumeResponse, error) {
	switch protocol {
	case ProtocolNFS:
		return s.setupNFSVolumeFromClone(ctx, req, clonedDataset, server, info)
	case ProtocolNVMeOF:
		return s.setupNVMeOFVolumeFromCloneWithValidation(ctx, req, clonedDataset, server, subsystemNQN, info)
	case ProtocolISCSI:
		return s.setupISCSIVolumeFromClone(ctx, req, clonedDataset, server, info)
	case ProtocolSMB:
		return s.setupSMBVolumeFromClone(ctx, req, clonedDataset, server, info)
	default:
		return s.handleUnknownProtocol(ctx, clonedDataset, protocol)
	}
}

// setupNVMeOFVolumeFromCloneWithValidation validates subsystemNQN and sets up NVMe-oF volume.
func (s *ControllerService) setupNVMeOFVolumeFromCloneWithValidation(ctx context.Context, req *csi.CreateVolumeRequest, clonedDataset *tnsapi.Dataset, server, subsystemNQN string, info *cloneInfo) (*csi.CreateVolumeResponse, error) {
	if subsystemNQN == "" {
		klog.Errorf("subsystemNQN parameter is required for NVMe-oF volumes, cleaning up")
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return nil, status.Error(codes.InvalidArgument,
			"subsystemNQN parameter is required for NVMe-oF volumes. "+
				"Pre-configure an NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
				"and provide its NQN in the StorageClass parameters.")
	}
	return s.setupNVMeOFVolumeFromClone(ctx, req, clonedDataset, server, subsystemNQN, info)
}

// handleUnknownProtocol handles the case when protocol is not recognized.
func (s *ControllerService) handleUnknownProtocol(ctx context.Context, clonedDataset *tnsapi.Dataset, protocol string) (*csi.CreateVolumeResponse, error) {
	klog.Errorf("Unknown protocol %s in snapshot metadata, cleaning up", protocol)
	if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
		klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
	}
	return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol in snapshot: %s", protocol)
}

// getVolumeParametersForSnapshot extracts server and subsystemNQN parameters
// from either the request parameters (StorageClass) or the source volume metadata.
func (s *ControllerService) getVolumeParametersForSnapshot(
	ctx context.Context,
	params map[string]string,
	snapshotMeta *SnapshotMetadata,
	clonedDataset *tnsapi.Dataset,
) (server, subsystemNQN string, err error) {
	// First try to get from request parameters (StorageClass)
	server = params["server"]
	subsystemNQN = params["subsystemNQN"]

	// If not provided in parameters, extract from source volume metadata
	needsSourceExtraction := server == "" || (snapshotMeta.Protocol == ProtocolNVMeOF && subsystemNQN == "")
	if !needsSourceExtraction {
		// All required parameters are available
		return server, subsystemNQN, s.validateServerParameter(ctx, server, clonedDataset)
	}

	klog.V(4).Infof("Server or subsystemNQN not in parameters, will derive from context (source volume: %s)", snapshotMeta.SourceVolume)

	// For NFS, server should be provided in StorageClass parameters
	// For NVMe-oF, we can try to find the subsystem NQN from TrueNAS
	if server == "" {
		// Server must come from StorageClass - we can't discover it
		klog.Errorf("Server parameter is required but not provided in StorageClass, cleaning up")
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return "", "", status.Error(codes.InvalidArgument,
			"server parameter is required in StorageClass for restoring from snapshot")
	}

	// For NVMe-oF with independent subsystems, we generate a new NQN for each clone.
	// The source volume's NQN is not needed - the clone gets its own dedicated subsystem.
	// We use a placeholder value to satisfy the validation; setupNVMeOFVolumeFromClone
	// will generate the actual NQN based on the new volume name.
	if subsystemNQN == "" && snapshotMeta.Protocol == ProtocolNVMeOF {
		// For clone operations, we don't need the source volume's subsystemNQN.
		// Each cloned volume gets its own independent subsystem with a newly generated NQN.
		// This allows restoring from detached snapshots even after the source volume is deleted.
		klog.V(4).Infof("NVMe-oF clone: will generate new subsystem NQN for cloned volume (source volume NQN not required)")
		subsystemNQN = "clone-will-generate-new-nqn" // Placeholder to pass validation
	}

	return server, subsystemNQN, s.validateServerParameter(ctx, server, clonedDataset)
}

// validateServerParameter validates that the server parameter is not empty.
func (s *ControllerService) validateServerParameter(ctx context.Context, server string, clonedDataset *tnsapi.Dataset) error {
	if server == "" {
		// Cleanup the cloned dataset
		klog.Errorf("server parameter is required, cleaning up")
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return status.Error(codes.InvalidArgument, "server parameter is required")
	}
	return nil
}
