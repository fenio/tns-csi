package driver

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// encodeSnapshotToken encodes an offset as a pagination token.
func encodeSnapshotToken(offset int) string {
	return strconv.Itoa(offset)
}

// parseSnapshotToken parses a pagination token to extract the offset.
func parseSnapshotToken(token string) (int, error) {
	var offset int
	_, err := fmt.Sscanf(token, "%d", &offset)
	if err != nil {
		return 0, fmt.Errorf("invalid token format: %w", err)
	}
	return offset, nil
}

// ListSnapshots lists snapshots.
func (s *ControllerService) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots called with request: %+v", req)

	// Special case: If filtering by snapshot ID, we can decode it and return directly if it exists
	if req.GetSnapshotId() != "" {
		return s.listSnapshotByID(ctx, req)
	}

	// Special case: If filtering by source volume ID, we need to decode the volume
	if req.GetSourceVolumeId() != "" {
		return s.listSnapshotsBySourceVolume(ctx, req)
	}

	// General case: list all snapshots (not commonly used, but required by CSI spec)
	return s.listAllSnapshots(ctx, req)
}

// ControllerGetSnapshot returns information about a specific snapshot.
// This is a CSI 1.12+ capability that provides a more efficient way to get a single snapshot
// compared to ListSnapshots with a snapshot_id filter.
func (s *ControllerService) ControllerGetSnapshot(ctx context.Context, req *csi.GetSnapshotRequest) (*csi.GetSnapshotResponse, error) {
	klog.V(4).Infof("ControllerGetSnapshot called with request: %+v", req)

	snapshotID := req.GetSnapshotId()
	if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID is required")
	}

	// Reuse ListSnapshots logic which already handles all snapshot types
	listResp, err := s.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
		SnapshotId: snapshotID,
	})
	if err != nil {
		return nil, err
	}

	// ListSnapshots returns empty list if not found, but GetSnapshot should return NotFound
	if len(listResp.Entries) == 0 {
		return nil, status.Errorf(codes.NotFound, "Snapshot %s not found", snapshotID)
	}

	return &csi.GetSnapshotResponse{
		Snapshot: listResp.Entries[0].Snapshot,
	}, nil
}

// listSnapshotByID handles listing a specific snapshot by ID.
func (s *ControllerService) listSnapshotByID(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	snapshotMeta, err := decodeSnapshotID(req.GetSnapshotId())
	if err != nil {
		// If snapshot ID is malformed, return empty list (snapshot doesn't exist)
		klog.V(4).Infof("Invalid snapshot ID %q: %v - returning empty list", req.GetSnapshotId(), err)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	// Handle detached snapshots differently - they are datasets, not ZFS snapshots
	if snapshotMeta.Detached {
		return s.listDetachedSnapshotByID(ctx, req, snapshotMeta)
	}

	// Regular snapshot: resolve the full ZFS snapshot name if we only have the short name
	zfsSnapshotName, err := s.resolveZFSSnapshotName(ctx, snapshotMeta)
	if err != nil {
		// Snapshot not found
		klog.V(4).Infof("Snapshot not found: %v - returning empty list", err)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	klog.V(4).Infof("ListSnapshots: filtering by snapshot ID (ZFS name: %s)", zfsSnapshotName)

	// Query to verify snapshot exists
	filters := []interface{}{
		[]interface{}{"id", "=", zfsSnapshotName},
	}

	snapshots, err := s.apiClient.QuerySnapshots(ctx, filters)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query snapshots: %v", err)
	}

	klog.V(4).Infof("Found %d snapshots after filtering", len(snapshots))

	if len(snapshots) == 0 {
		// Snapshot doesn't exist, return empty list
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	// Query source volume capacity for SizeBytes
	var sizeBytes int64
	sourceVolumeID := snapshotMeta.SourceVolume
	if isDatasetPathVolumeID(sourceVolumeID) {
		ds, dsErr := s.apiClient.GetDatasetWithProperties(ctx, sourceVolumeID)
		if dsErr == nil && ds != nil {
			if capProp, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
				sizeBytes = tnsapi.StringToInt64(capProp.Value)
			}
			if sizeBytes == 0 {
				sizeBytes = getZvolCapacity(&ds.Dataset)
			}
		}
	}

	// Snapshot exists - return it with the metadata we decoded
	// (which includes protocol, source volume, etc.)
	entry := &csi.ListSnapshotsResponse_Entry{
		Snapshot: &csi.Snapshot{
			SnapshotId:     req.GetSnapshotId(), // Return the same ID we were queried with
			SourceVolumeId: snapshotMeta.SourceVolume,
			CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
			ReadyToUse:     true,
			SizeBytes:      sizeBytes,
		},
	}

	return &csi.ListSnapshotsResponse{
		Entries: []*csi.ListSnapshotsResponse_Entry{entry},
	}, nil
}

// listDetachedSnapshotByID handles listing a specific detached snapshot by ID.
// Detached snapshots are stored as datasets, so we use property-based lookup.
func (s *ControllerService) listDetachedSnapshotByID(ctx context.Context, req *csi.ListSnapshotsRequest, snapshotMeta *SnapshotMetadata) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots: looking up detached snapshot %s via properties", snapshotMeta.SnapshotName)

	// Use property-based lookup to find the detached snapshot dataset
	resolvedMeta, err := s.lookupSnapshotByCSIName(ctx, "", snapshotMeta.SnapshotName)
	if err != nil {
		klog.Warningf("Failed to lookup detached snapshot %s: %v", snapshotMeta.SnapshotName, err)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	if resolvedMeta == nil {
		// Snapshot not found
		klog.V(4).Infof("Detached snapshot %s not found - returning empty list", snapshotMeta.SnapshotName)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	klog.V(4).Infof("Found detached snapshot %s at dataset %s", snapshotMeta.SnapshotName, resolvedMeta.DatasetName)

	// Query source volume capacity for SizeBytes
	var sizeBytes int64
	if resolvedMeta.SourceVolume != "" && isDatasetPathVolumeID(resolvedMeta.SourceVolume) {
		ds, dsErr := s.apiClient.GetDatasetWithProperties(ctx, resolvedMeta.SourceVolume)
		if dsErr == nil && ds != nil {
			if capProp, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
				sizeBytes = tnsapi.StringToInt64(capProp.Value)
			}
			if sizeBytes == 0 {
				sizeBytes = getZvolCapacity(&ds.Dataset)
			}
		}
	}

	// Snapshot exists - return it
	entry := &csi.ListSnapshotsResponse_Entry{
		Snapshot: &csi.Snapshot{
			SnapshotId:     req.GetSnapshotId(), // Return the same ID we were queried with
			SourceVolumeId: resolvedMeta.SourceVolume,
			CreationTime:   timestamppb.New(time.Now()), // We don't store creation time in properties
			ReadyToUse:     true,
			SizeBytes:      sizeBytes,
		},
	}

	return &csi.ListSnapshotsResponse{
		Entries: []*csi.ListSnapshotsResponse_Entry{entry},
	}, nil
}

// listSnapshotsBySourceVolume handles listing snapshots for a specific source volume.
func (s *ControllerService) listSnapshotsBySourceVolume(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()

	// Determine dataset name, protocol, and capacity for the source volume
	var datasetName string
	var protocol string
	var sizeBytes int64
	if isDatasetPathVolumeID(sourceVolumeID) {
		// New format: volume ID is the dataset path, use directly (O(1))
		datasetName = sourceVolumeID
		// Look up protocol and capacity from dataset properties
		dataset, err := s.apiClient.GetDatasetWithProperties(ctx, sourceVolumeID)
		if err == nil && dataset != nil {
			if prop, ok := dataset.UserProperties[tnsapi.PropertyProtocol]; ok {
				protocol = prop.Value
			}
			if capProp, ok := dataset.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
				sizeBytes = tnsapi.StringToInt64(capProp.Value)
			}
			if sizeBytes == 0 {
				sizeBytes = getZvolCapacity(&dataset.Dataset)
			}
		}
	} else {
		// Legacy format: plain volume name, search by shares/namespaces/extents
		result := s.discoverVolumeBySearching(ctx, sourceVolumeID)
		if result == nil {
			klog.V(4).Infof("Source volume %q not found in TrueNAS - returning empty list", sourceVolumeID)
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
		datasetName = result.datasetName
		protocol = result.protocol
	}

	// Query snapshots for this dataset (snapshots will have format dataset@snapname)
	filters := []interface{}{
		[]interface{}{verbDataset, "=", datasetName},
	}

	snapshots, err := s.apiClient.QuerySnapshots(ctx, filters)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query snapshots: %v", err)
	}

	klog.V(4).Infof("Found %d snapshots for volume %s", len(snapshots), req.GetSourceVolumeId())

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = len(snapshots)
	}

	startIndex := 0
	if req.GetStartingToken() != "" {
		startIndex, err = parseSnapshotToken(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "Invalid starting token: %v", err)
		}
		if startIndex < 0 || startIndex >= len(snapshots) {
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
	}

	endIndex := startIndex + maxEntries
	if endIndex > len(snapshots) {
		endIndex = len(snapshots)
	}

	// Convert to CSI format
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapshot := snapshots[i]

		// Create snapshot metadata - we know the source volume from the request
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			SourceVolume: req.GetSourceVolumeId(),
			DatasetName:  snapshot.Dataset,
			Protocol:     protocol,
			CreatedAt:    time.Now().Unix(),
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v", snapshot.ID, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: req.GetSourceVolumeId(),
				CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
				ReadyToUse:     true,
				SizeBytes:      sizeBytes,
			},
		}
		entries = append(entries, entry)
	}

	var nextToken string
	if endIndex < len(snapshots) {
		nextToken = encodeSnapshotToken(endIndex)
	}

	return &csi.ListSnapshotsResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}

// listAllSnapshots handles listing all snapshots (no filters).
// Only lists snapshots on CSI-managed datasets to avoid fetching all snapshots globally,
// which can cause buffer overflow and timeouts on systems with many non-CSI datasets.
func (s *ControllerService) listAllSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// Find all CSI-managed datasets first (small, filtered query)
	datasets, err := s.apiClient.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query managed datasets: %v", err)
	}

	// Build metadata map and collect snapshots per managed dataset
	type datasetMeta struct {
		volumeID      string
		protocol      string
		capacityBytes int64
	}
	managedMeta := make(map[string]datasetMeta, len(datasets))
	for _, ds := range datasets {
		// Skip detached snapshots (they're datasets, not volumes with snapshots)
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == VolumeContextValueTrue {
			continue
		}
		volumeID := ds.ID
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok && prop.Value != "" {
			volumeID = prop.Value
		}
		protocol := ProtocolNFS
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok && prop.Value != "" {
			protocol = prop.Value
		}
		var capacityBytes int64
		if capProp, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
			capacityBytes = tnsapi.StringToInt64(capProp.Value)
		}
		if capacityBytes == 0 {
			capacityBytes = getZvolCapacity(&ds.Dataset)
		}
		managedMeta[ds.ID] = datasetMeta{volumeID: volumeID, protocol: protocol, capacityBytes: capacityBytes}
	}

	// Query snapshots per managed dataset (each query is small and filtered)
	var allSnapshots []tnsapi.Snapshot
	for datasetID := range managedMeta {
		snaps, queryErr := s.apiClient.QuerySnapshots(ctx, []interface{}{
			[]interface{}{verbDataset, "=", datasetID},
		})
		if queryErr != nil {
			klog.Warningf("Failed to query snapshots for dataset %s: %v", datasetID, queryErr)
			continue
		}
		allSnapshots = append(allSnapshots, snaps...)
	}

	klog.V(4).Infof("Found %d total snapshots across %d managed datasets", len(allSnapshots), len(managedMeta))

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = len(allSnapshots)
	}

	startIndex := 0
	if req.GetStartingToken() != "" {
		startIndex, err = parseSnapshotToken(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "Invalid starting token: %v", err)
		}
		if startIndex < 0 || startIndex >= len(allSnapshots) {
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
	}

	endIndex := startIndex + maxEntries
	if endIndex > len(allSnapshots) {
		endIndex = len(allSnapshots)
	}

	// Convert to CSI format using metadata from managed datasets
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapshot := allSnapshots[i]

		meta, ok := managedMeta[snapshot.Dataset]
		if !ok {
			continue
		}

		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.Name,
			SourceVolume: meta.volumeID,
			DatasetName:  snapshot.Dataset,
			Protocol:     meta.protocol,
			CreatedAt:    time.Now().Unix(),
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v - skipping", snapshot.ID, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: meta.volumeID,
				CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
				ReadyToUse:     true,
				SizeBytes:      meta.capacityBytes,
			},
		}
		entries = append(entries, entry)
	}

	var nextToken string
	if endIndex < len(allSnapshots) {
		nextToken = encodeSnapshotToken(endIndex)
	}

	return &csi.ListSnapshotsResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}
