package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// createDetachedSnapshot creates a detached snapshot using zfs send/receive via TrueNAS replication API.
// Detached snapshots are stored as full dataset copies, independent of the source volume.
// They survive deletion of the source volume, making them suitable for backup/DR scenarios.
func (s *ControllerService) createDetachedSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotName, sourceVolumeID, sourceDataset, protocol, pool, detachedParentDataset string, sizeBytes int64) (*csi.CreateSnapshotResponse, error) {
	// Determine the parent dataset for detached snapshots
	if detachedParentDataset == "" {
		if pool == "" {
			// Extract pool from source dataset
			parts := strings.Split(sourceDataset, "/")
			if len(parts) > 0 {
				pool = parts[0]
			}
		}
		if pool == "" {
			timer.ObserveError()
			return nil, status.Errorf(codes.InvalidArgument,
				"Cannot determine pool for detached snapshots. Specify '%s' in VolumeSnapshotClass parameters",
				DetachedSnapshotsParentDatasetParam)
		}
		detachedParentDataset = fmt.Sprintf("%s/%s", pool, DefaultDetachedSnapshotsFolder)
	}

	// Ensure the parent dataset exists (creates it if not)
	if err := s.ensureDetachedSnapshotsParentDataset(ctx, detachedParentDataset); err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to ensure detached snapshots parent dataset %s exists: %v", detachedParentDataset, err)
	}

	// Target dataset for the detached snapshot
	targetDataset := fmt.Sprintf("%s/%s", detachedParentDataset, snapshotName)

	klog.Infof("Creating detached snapshot %s for volume %s (source: %s, target: %s, protocol: %s)",
		snapshotName, sourceVolumeID, sourceDataset, targetDataset, protocol)

	// Check if detached snapshot already exists (idempotency)
	existingDatasets, err := s.apiClient.QueryAllDatasets(ctx, targetDataset)
	if err != nil {
		klog.Warningf("Failed to query existing datasets: %v", err)
	}

	for _, ds := range existingDatasets {
		if ds.Name != targetDataset {
			continue
		}
		klog.Infof("Detached snapshot dataset %s already exists", targetDataset)

		// Create snapshot metadata
		createdAt := time.Now().Unix()
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshotName,
			SourceVolume: sourceVolumeID,
			DatasetName:  targetDataset,
			Protocol:     protocol,
			CreatedAt:    createdAt,
			Detached:     true,
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to encode snapshot ID: %v", encodeErr)
		}

		timer.ObserveSuccess()
		return &csi.CreateSnapshotResponse{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: sourceVolumeID,
				CreationTime:   timestamppb.New(time.Unix(createdAt, 0)),
				ReadyToUse:     true,
				SizeBytes:      sizeBytes,
			},
		}, nil
	}

	// Step 1: Create a temporary ZFS snapshot on the source
	tempSnapshotName := fmt.Sprintf("csi-detached-temp-%d", time.Now().UnixNano())
	tempSnapshot := fmt.Sprintf("%s@%s", sourceDataset, tempSnapshotName)

	klog.V(4).Infof("Creating temporary snapshot %s for detached copy", tempSnapshot)

	_, err = s.apiClient.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{
		Dataset:   sourceDataset,
		Name:      tempSnapshotName,
		Recursive: false,
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot for detached copy: %v", err)
	}

	// Ensure we clean up the temporary snapshot
	defer func() {
		klog.V(4).Infof("Cleaning up temporary snapshot %s", tempSnapshot)
		if delErr := s.apiClient.DeleteSnapshot(ctx, tempSnapshot); delErr != nil {
			klog.Warningf("Failed to delete temporary snapshot %s: %v", tempSnapshot, delErr)
		}
	}()

	// Step 2: Run one-time replication (zfs send/receive) to create the detached copy
	klog.V(4).Infof("Running one-time replication from %s to %s", sourceDataset, targetDataset)

	replicationParams := tnsapi.ReplicationRunOnetimeParams{
		Direction:               "PUSH",
		Transport:               "LOCAL",
		SourceDatasets:          []string{sourceDataset},
		TargetDataset:           targetDataset,
		Recursive:               false,
		Properties:              true,
		PropertiesExclude:       []string{"mountpoint", "sharenfs", "sharesmb", tnsapi.PropertyCSIVolumeName},
		Replicate:               false,
		Encryption:              false,
		NameRegex:               &tempSnapshotName,
		NamingSchema:            []string{},
		AlsoIncludeNamingSchema: []string{},
		RetentionPolicy:         "NONE",
		Readonly:                "IGNORE",
		AllowFromScratch:        true,
	}

	err = s.apiClient.RunOnetimeReplicationAndWait(ctx, replicationParams, ReplicationPollInterval)
	if err != nil {
		timer.ObserveError()
		// Try to clean up the target dataset if it was partially created
		klog.Warningf("Detached snapshot replication failed: %v. Attempting cleanup of %s", err, targetDataset)
		if delErr := s.apiClient.DeleteDataset(ctx, targetDataset); delErr != nil {
			klog.Warningf("Failed to cleanup partial detached snapshot dataset: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create detached snapshot via replication: %v", err)
	}

	klog.Infof("Replication completed for detached snapshot dataset: %s", targetDataset)

	// Step 3: Attempt to promote the target dataset to break clone dependency
	// TrueNAS LOCAL replication creates clone relationships for efficiency (instant, space-efficient).
	// Promotion breaks the clone->origin dependency, allowing the source volume to be deleted later.
	// Without promotion, deleting the source will fail with "volume has dependent clones".
	klog.Infof("Attempting to promote detached snapshot dataset %s to break clone dependency", targetDataset)
	if promoteErr := s.apiClient.PromoteDataset(ctx, targetDataset); promoteErr != nil {
		// Log the full error for debugging - this helps identify why promotion failed
		klog.Warningf("PromoteDataset(%s) failed: %v", targetDataset, promoteErr)
		klog.Warningf("Promotion failure may cause source volume deletion to fail later with 'dependent clones' error")
		// Continue anyway - snapshot creation can still succeed, but source deletion may be blocked
	} else {
		klog.Infof("Successfully promoted detached snapshot dataset: %s (clone dependency broken)", targetDataset)
	}

	// Step 4: Clean up the temporary snapshot that was replicated to the target
	// The replication copies the snapshot to the target, so we need to remove it
	targetTempSnapshot := fmt.Sprintf("%s@%s", targetDataset, tempSnapshotName)
	klog.V(4).Infof("Cleaning up replicated temporary snapshot %s", targetTempSnapshot)
	if delErr := s.apiClient.DeleteSnapshot(ctx, targetTempSnapshot); delErr != nil {
		klog.Warningf("Failed to delete replicated temporary snapshot %s: %v", targetTempSnapshot, delErr)
	}

	// Step 5: Set CSI metadata properties on the detached snapshot dataset
	props := map[string]string{
		tnsapi.PropertyManagedBy:        tnsapi.ManagedByValue,
		tnsapi.PropertySnapshotID:       snapshotName,
		tnsapi.PropertySourceVolumeID:   sourceVolumeID,
		tnsapi.PropertyDetachedSnapshot: VolumeContextValueTrue,
		tnsapi.PropertySourceDataset:    sourceDataset,
		tnsapi.PropertyProtocol:         protocol,
		tnsapi.PropertyDeleteStrategy:   verbDelete,
	}
	if s.clusterID != "" {
		props[tnsapi.PropertyClusterID] = s.clusterID
	}
	if err := s.apiClient.SetDatasetProperties(ctx, targetDataset, props); err != nil {
		// Property setting is critical - without PropertySnapshotID, the snapshot can't be found
		// during restore operations. We must clean up and fail.
		klog.Errorf("Failed to set CSI properties on detached snapshot dataset %s: %v. Cleaning up.", targetDataset, err)
		if delErr := s.apiClient.DeleteDataset(ctx, targetDataset); delErr != nil {
			klog.Errorf("Failed to cleanup detached snapshot dataset after property setting failure: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to set CSI properties on detached snapshot: %v", err)
	}

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshotName,
		SourceVolume: sourceVolumeID,
		DatasetName:  targetDataset,
		Protocol:     protocol,
		CreatedAt:    createdAt,
		Detached:     true,
	}

	snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
	if encodeErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to encode snapshot ID: %v", encodeErr)
	}

	timer.ObserveSuccess()
	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snapshotID,
			SourceVolumeId: sourceVolumeID,
			CreationTime:   timestamppb.New(time.Unix(createdAt, 0)),
			ReadyToUse:     true,
			SizeBytes:      sizeBytes,
		},
	}, nil
}

// ensureDetachedSnapshotsParentDataset ensures the parent dataset for detached snapshots exists.
// Creates it if it doesn't exist and marks it as managed by tns-csi.
// This keeps detached snapshot datasets separate from volume datasets (democratic-csi pattern).
func (s *ControllerService) ensureDetachedSnapshotsParentDataset(ctx context.Context, parentDataset string) error {
	klog.V(4).Infof("Ensuring detached snapshots parent dataset exists: %s", parentDataset)

	// Check if the dataset already exists
	datasets, err := s.apiClient.QueryAllDatasets(ctx, parentDataset)
	if err != nil {
		return fmt.Errorf("failed to query dataset %s: %w", parentDataset, err)
	}

	for _, ds := range datasets {
		if ds.Name == parentDataset || ds.ID == parentDataset {
			klog.V(4).Infof("Detached snapshots parent dataset already exists: %s", parentDataset)
			return nil
		}
	}

	// Dataset doesn't exist - create it
	klog.Infof("Creating detached snapshots parent dataset: %s", parentDataset)

	createParams := tnsapi.DatasetCreateParams{
		Name: parentDataset,
		Type: datasetTypeFilesystem,
	}

	_, err = s.apiClient.CreateDataset(ctx, createParams)
	if err != nil {
		return fmt.Errorf("failed to create parent dataset %s: %w", parentDataset, err)
	}

	// Set properties to mark it as managed by tns-csi
	props := map[string]string{
		tnsapi.PropertyManagedBy: tnsapi.ManagedByValue,
	}
	if propErr := s.apiClient.SetDatasetProperties(ctx, parentDataset, props); propErr != nil {
		klog.Warningf("Failed to set properties on parent dataset %s: %v (non-fatal)", parentDataset, propErr)
	}

	klog.Infof("Successfully created detached snapshots parent dataset: %s", parentDataset)
	return nil
}

// deleteDetachedSnapshot deletes a detached snapshot dataset.
// Detached snapshots are stored as full dataset copies, so we delete the dataset instead of a ZFS snapshot.
func (s *ControllerService) deleteDetachedSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotMeta *SnapshotMetadata) (*csi.DeleteSnapshotResponse, error) {
	// For detached snapshots, DatasetName contains the full dataset path
	// For compact format, DatasetName is empty - use property-based lookup to find it
	datasetPath := snapshotMeta.DatasetName

	if datasetPath == "" {
		// Compact format doesn't include DatasetName - use property-based lookup
		klog.V(4).Infof("DatasetName empty for detached snapshot %s, using property-based lookup", snapshotMeta.SnapshotName)

		// Search across all pools for the detached snapshot dataset by its snapshot ID property
		resolvedMeta, err := s.lookupSnapshotByCSIName(ctx, "", snapshotMeta.SnapshotName)
		if err != nil {
			klog.Warningf("Failed to lookup detached snapshot %s via properties: %v", snapshotMeta.SnapshotName, err)
			// Continue anyway - we'll try to delete by constructed path below
		} else if resolvedMeta != nil {
			datasetPath = resolvedMeta.DatasetName
			klog.V(4).Infof("Resolved detached snapshot %s to dataset: %s", snapshotMeta.SnapshotName, datasetPath)
		}
	}

	// If we still don't have a dataset path, the snapshot likely doesn't exist
	if datasetPath == "" {
		klog.Infof("Could not resolve dataset path for detached snapshot %s, assuming already deleted", snapshotMeta.SnapshotName)
		timer.ObserveSuccess()
		return &csi.DeleteSnapshotResponse{}, nil
	}

	klog.Infof("Deleting detached snapshot dataset: %s (snapshot: %s)", datasetPath, snapshotMeta.SnapshotName)

	// Verify this is actually a detached snapshot by checking properties (if dataset exists)
	props, err := s.apiClient.GetDatasetProperties(ctx, datasetPath, []string{tnsapi.PropertyDetachedSnapshot, tnsapi.PropertyManagedBy})
	if err != nil {
		// If dataset doesn't exist, consider deletion successful (idempotent)
		if isNotFoundError(err) {
			klog.Infof("Detached snapshot dataset %s not found, assuming already deleted", datasetPath)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		// Log warning but continue - we'll try to delete anyway
		klog.Warningf("Failed to get properties for detached snapshot dataset %s: %v", datasetPath, err)
	} else {
		// Verify it's a tns-csi managed detached snapshot
		if props[tnsapi.PropertyManagedBy] != tnsapi.ManagedByValue {
			klog.Warningf("Dataset %s is not managed by tns-csi (managed_by=%s), refusing to delete",
				datasetPath, props[tnsapi.PropertyManagedBy])
			timer.ObserveError()
			return nil, status.Errorf(codes.FailedPrecondition,
				"Dataset %s is not managed by tns-csi", datasetPath)
		}
		if props[tnsapi.PropertyDetachedSnapshot] != VolumeContextValueTrue {
			klog.Warningf("Dataset %s is not marked as a detached snapshot, refusing to delete", datasetPath)
			timer.ObserveError()
			return nil, status.Errorf(codes.FailedPrecondition,
				"Dataset %s is not a detached snapshot", datasetPath)
		}
	}

	// Delete the dataset
	if err := s.apiClient.DeleteDataset(ctx, datasetPath); err != nil {
		// Check if error is because dataset doesn't exist
		if isNotFoundError(err) {
			klog.Infof("Detached snapshot dataset %s not found, assuming already deleted", datasetPath)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete detached snapshot dataset: %v", err)
	}

	klog.Infof("Successfully deleted detached snapshot dataset: %s", datasetPath)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}
