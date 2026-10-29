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
	// The temporary snapshot sent to the target is named after the CSI snapshot, so that a
	// retry knows which snapshot an earlier attempt sent and has to clean up.
	tempSnapshotName := "csi-detached-temp-" + snapshotName
	tempSnapshot := fmt.Sprintf("%s@%s", sourceDataset, tempSnapshotName)

	klog.Infof("Creating detached snapshot %s for volume %s (source: %s, target: %s, protocol: %s)",
		snapshotName, sourceVolumeID, sourceDataset, targetDataset, protocol)

	// A retried CreateSnapshot (the snapshotter retries after its timeout) may find the
	// replication an earlier attempt started still running, failed, or finished.
	received, err := s.settleDetachedSnapshotReplication(ctx, snapshotName, targetDataset)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	if received {
		klog.Infof("Detached snapshot dataset %s was received by an earlier attempt; finishing it", targetDataset)
		s.finishDetachedReplication(ctx, targetDataset, tempSnapshotName)
		props := s.detachedSnapshotProperties(snapshotName, sourceVolumeID, sourceDataset, protocol)
		if err = s.apiClient.SetDatasetProperties(ctx, targetDataset, props); err != nil {
			// The complete copy is kept: the next retry finishes it again.
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to set CSI properties on detached snapshot: %v", err)
		}
		s.deleteSnapshotIfPresent(ctx, tempSnapshot)
		return detachedSnapshotResponse(timer, snapshotName, sourceVolumeID, targetDataset, protocol, sizeBytes)
	}

	// Step 1: Create a temporary ZFS snapshot on the source. An earlier attempt whose
	// replication failed may have kept it; it is sent again.
	klog.V(4).Infof("Creating temporary snapshot %s for detached copy", tempSnapshot)

	_, err = s.apiClient.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{
		Dataset:   sourceDataset,
		Name:      tempSnapshotName,
		Recursive: false,
	})
	if err != nil && !tnsapi.IsAlreadyExists(err) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot for detached copy: %v", err)
	}

	// Ensure we clean up the temporary snapshot, unless a replication may still be sending it.
	replicationMayBeRunning := false
	defer func() {
		if replicationMayBeRunning {
			klog.Warningf("Keeping temporary snapshot %s: the replication sending it may still be running", tempSnapshot)
			return
		}
		klog.V(4).Infof("Cleaning up temporary snapshot %s", tempSnapshot)
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		if delErr := s.apiClient.DeleteSnapshot(cleanupCtx, tempSnapshot); delErr != nil {
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
		if ctx.Err() != nil {
			// Only the wait ended: the job keeps running on TrueNAS, so neither its target nor
			// the snapshot it sends may be deleted (that would make every retry start over).
			replicationMayBeRunning = true
			klog.Warningf("Stopped waiting for the detached snapshot replication into %s: %v (the job may still be running)", targetDataset, err)
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		// Try to clean up the target dataset if it was partially created
		klog.Warningf("Detached snapshot replication failed: %v. Attempting cleanup of %s", err, targetDataset)
		cleanupCtx, cancel := cleanupContext(ctx)
		if delErr := s.apiClient.DeleteDataset(cleanupCtx, targetDataset); delErr != nil {
			klog.Warningf("Failed to cleanup partial detached snapshot dataset: %v", delErr)
		}
		cancel()
		return nil, status.Errorf(codes.Internal, "Failed to create detached snapshot via replication: %v", err)
	}

	klog.Infof("Replication completed for detached snapshot dataset: %s", targetDataset)

	// Steps 3 and 4: promote the target to break any clone dependency on the source (without
	// it, deleting the source fails with "dependent clones") and delete the temporary snapshot
	// the replication copied to it.
	s.finishDetachedReplication(ctx, targetDataset, tempSnapshotName)

	// Step 5: Set CSI metadata properties on the detached snapshot dataset
	props := s.detachedSnapshotProperties(snapshotName, sourceVolumeID, sourceDataset, protocol)
	if err := s.apiClient.SetDatasetProperties(ctx, targetDataset, props); err != nil {
		// Property setting is critical - without PropertySnapshotID, the snapshot can't be found
		// during restore operations. We must clean up and fail.
		klog.Errorf("Failed to set CSI properties on detached snapshot dataset %s: %v. Cleaning up.", targetDataset, err)
		cleanupCtx, cancel := cleanupContext(ctx)
		if delErr := s.apiClient.DeleteDataset(cleanupCtx, targetDataset); delErr != nil {
			klog.Errorf("Failed to cleanup detached snapshot dataset after property setting failure: %v", delErr)
		}
		cancel()
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to set CSI properties on detached snapshot: %v", err)
	}

	return detachedSnapshotResponse(timer, snapshotName, sourceVolumeID, targetDataset, protocol, sizeBytes)
}

// settleDetachedSnapshotReplication deals with what an earlier attempt of this CreateSnapshot
// left at target. TrueNAS keeps running a replication after the request that started it
// timed out, so the target of a retry may hold a partial copy. A running job is waited for.
// The target of a failed job, or one still holding a receive_resume_token (an interrupted
// receive whose job middlewared may have forgotten), is discarded so the copy starts again.
// received reports a complete receive whose remaining steps the caller must (re)run. A
// dataset there that belongs to anything else is AlreadyExists and is never changed.
func (s *ControllerService) settleDetachedSnapshotReplication(ctx context.Context, snapshotName, target string) (received bool, err error) {
	failed, reason, err := s.awaitEarlierReplication(ctx, target)
	if err != nil {
		return false, err
	}

	existing, err := s.queryExactDataset(ctx, target)
	if err != nil {
		return false, status.Errorf(codes.Internal, "Failed to check for existing dataset %s: %v", target, err)
	}
	if len(existing) == 0 {
		return false, nil
	}
	if exists, err := s.checkOwnDetachedSnapshotTarget(ctx, snapshotName, target); err != nil || !exists {
		return false, err
	}
	if failed || existing[0].ReceiveIncomplete() {
		if !failed {
			reason += " and the receive was interrupted (the target still holds a receive_resume_token)"
		}
		return false, s.discardFailedReceive(ctx, target, reason)
	}
	return true, nil
}

// checkOwnDetachedSnapshotTarget verifies that the dataset at target may be this snapshot's:
// a receive copies the source volume's managed_by and, at most, this snapshot's own
// properties from an earlier attempt. User properties may only land at the end of a receive,
// so managed_by may be missing, but it must not name another owner; a snapshot_id must be
// this snapshot's, and csi_volume_name (never replicated) must be empty. exists is false when
// the dataset is gone.
func (s *ControllerService) checkOwnDetachedSnapshotTarget(ctx context.Context, snapshotName, target string) (exists bool, err error) {
	ownership, err := s.readVolumeOwnership(ctx, &VolumeMetadata{Name: snapshotName, DatasetID: target}, tnsapi.PropertySnapshotID)
	switch {
	case status.Code(err) == codes.FailedPrecondition:
		return false, status.Errorf(codes.AlreadyExists,
			"dataset %s already exists and does not belong to snapshot %s: %s", target, snapshotName, status.Convert(err).Message())
	case err != nil:
		return false, err
	case ownership.notFound:
		return false, nil
	}
	managedBy := ownership.props[tnsapi.PropertyManagedBy]
	snapshotID := ownership.props[tnsapi.PropertySnapshotID]
	volumeName := ownership.props[tnsapi.PropertyCSIVolumeName]
	if (snapshotID != "" && snapshotID != snapshotName) || volumeName != "" {
		return false, status.Errorf(codes.AlreadyExists,
			"dataset %s already exists and is not snapshot %s (%s=%q, %s=%q, %s=%q)", target, snapshotName,
			tnsapi.PropertyManagedBy, managedBy, tnsapi.PropertySnapshotID, snapshotID, tnsapi.PropertyCSIVolumeName, volumeName)
	}
	return true, nil
}

// detachedSnapshotProperties returns the CSI properties written on a detached snapshot dataset.
func (s *ControllerService) detachedSnapshotProperties(snapshotName, sourceVolumeID, sourceDataset, protocol string) map[string]string {
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
	return props
}

// detachedSnapshotResponse reports a finished detached snapshot as ready.
func detachedSnapshotResponse(timer *metrics.OperationTimer, snapshotName, sourceVolumeID, targetDataset, protocol string, sizeBytes int64) (*csi.CreateSnapshotResponse, error) {
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
	datasets, err := s.queryExactDataset(ctx, parentDataset)
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
		if tnsapi.IsNotFound(err) {
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
		if tnsapi.IsNotFound(err) {
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
