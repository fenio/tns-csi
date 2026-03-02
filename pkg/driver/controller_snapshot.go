package driver

import (
	"context"
	"errors"
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

// Snapshot and clone configuration constants.
//
// Volume Clone Modes (StorageClass parameters):
//
// 1. Default (no parameter): Standard ZFS COW clone
//   - Clone depends on snapshot (snapshot cannot be deleted while clone exists)
//   - Most space-efficient, instant creation
//
// 2. promotedVolumesFromSnapshots/promotedVolumesFromVolumes = "true": ZFS clone + promote
//   - Reversed dependency (source depends on clone after promotion)
//   - Allows deleting the original snapshot, but clone cannot be deleted while source exists
//   - Instant creation, useful for snapshot rotation
//
// 3. detachedVolumesFromSnapshots/detachedVolumesFromVolumes = "true": ZFS send/receive
//   - Truly independent copy with NO dependency in either direction
//   - Slower (full data copy), but complete independence
//   - Both source and clone can be deleted in any order
const (
	// DetachedSnapshotsParam is the VolumeSnapshotClass parameter to enable detached snapshots.
	// When true, snapshots are created via zfs send/receive as independent datasets.
	DetachedSnapshotsParam = "detachedSnapshots"

	// PromotedVolumesFromSnapshotsParam is the StorageClass parameter to create promoted clones
	// when restoring from snapshots. Uses zfs clone + zfs promote.
	// After promotion, the dependency is REVERSED: source depends on clone.
	// This allows deleting the original snapshot, but the clone cannot be deleted
	// while the source volume exists.
	PromotedVolumesFromSnapshotsParam = "promotedVolumesFromSnapshots"

	// PromotedVolumesFromVolumesParam is the StorageClass parameter to create promoted clones
	// when cloning from volumes. Uses zfs clone + zfs promote on the temp snapshot.
	// After promotion, the temp snapshot is deleted and dependency is reversed.
	PromotedVolumesFromVolumesParam = "promotedVolumesFromVolumes"

	// DetachedVolumesFromSnapshotsParam is the StorageClass parameter to create truly independent
	// volumes when restoring from snapshots. Uses zfs send/receive for a full data copy.
	// The resulting volume has NO dependency on the source snapshot.
	// Slower than clone+promote but provides complete independence.
	DetachedVolumesFromSnapshotsParam = "detachedVolumesFromSnapshots"

	// DetachedVolumesFromVolumesParam is the StorageClass parameter to create truly independent
	// volumes when cloning from volumes. Uses zfs send/receive for a full data copy.
	// The resulting volume has NO dependency on the source volume.
	// Slower than clone+promote but provides complete independence.
	DetachedVolumesFromVolumesParam = "detachedVolumesFromVolumes"

	// VolumeSourceSnapshotPrefix is the prefix for temporary snapshots created during volume-to-volume
	// cloning. Uses the same naming convention as democratic-csi for compatibility.
	VolumeSourceSnapshotPrefix = "volume-source-for-volume-"

	// DetachedSnapshotsParentDatasetParam is the VolumeSnapshotClass parameter for the parent dataset
	// where detached snapshots will be stored. If not specified, defaults to {pool}/csi-detached-snapshots.
	DetachedSnapshotsParentDatasetParam = "detachedSnapshotsParentDataset"

	// DetachedSnapshotPrefix is the prefix used in snapshot IDs to identify detached snapshots.
	// Format: detached:{protocol}:{volume_id}@{snapshot_name}.
	DetachedSnapshotPrefix = "detached:"

	// DefaultDetachedSnapshotsFolder is the default folder name for detached snapshots.
	DefaultDetachedSnapshotsFolder = "csi-detached-snapshots"

	// ReplicationPollInterval is the interval for polling replication job status.
	ReplicationPollInterval = 2 * time.Second
)

// Static errors for snapshot operations.
var (
	ErrProtocolRequired             = errors.New("protocol is required for snapshot ID encoding")
	ErrSourceVolumeRequired         = errors.New("source volume is required for snapshot ID encoding")
	ErrSnapshotNameRequired         = errors.New("snapshot name is required for snapshot ID encoding")
	ErrInvalidSnapshotIDFormat      = errors.New("invalid compact snapshot ID format")
	ErrInvalidProtocol              = errors.New("invalid protocol in snapshot ID")
	ErrSnapshotNotFoundTrueNAS      = errors.New("snapshot not found in TrueNAS")
	ErrDetachedSnapshotFailed       = errors.New("detached snapshot creation failed")
	ErrDetachedParentDatasetMissing = errors.New("detached snapshots parent dataset is required")
	ErrDetachedSnapshotNotFound     = errors.New("detached snapshot not found")
)

// SnapshotMetadata contains information needed to manage a snapshot.
type SnapshotMetadata struct {
	SnapshotName string `json:"snapshotName"` // ZFS snapshot name (dataset@snapshot) or detached dataset name
	SourceVolume string `json:"sourceVolume"` // Source volume ID
	DatasetName  string `json:"datasetName"`  // Parent dataset name (source for regular, target for detached)
	Protocol     string `json:"protocol"`     // Protocol (nfs, nvmeof, iscsi)
	CreatedAt    int64  `json:"-"`            // Creation timestamp (Unix epoch) - excluded from ID encoding
	Detached     bool   `json:"-"`            // True if this is a detached snapshot (stored as dataset, not ZFS snapshot)
}

// Compact snapshot ID format: {protocol}:{volume_id}@{snapshot_name}.
// Example: "nfs:pvc-abc123@snap-xyz789" (~65 bytes vs 300+ for base64 JSON).
// This format is CSI-compliant (under 128 bytes) and easy to parse.
//
// Detached snapshot ID format: detached:{protocol}:{volume_id}@{snapshot_name}
// Example: "detached:nfs:pvc-abc123@snap-xyz789"
// Detached snapshots are stored as full dataset copies via zfs send/receive,
// independent of the source volume (survive source deletion).
//
// The full ZFS dataset path can be reconstructed from:
// - parentDataset (from StorageClass parameters) + volumeID.
// - Format: {parentDataset}/{volumeID}@{snapshotName}.

// encodeSnapshotID encodes snapshot metadata into a compact snapshotID string.
// Format: {protocol}:{volume_id}@{snapshot_name} or detached:{protocol}:{volume_id}@{snapshot_name}.
func encodeSnapshotID(meta SnapshotMetadata) (string, error) {
	if meta.Protocol == "" {
		return "", ErrProtocolRequired
	}
	if meta.SourceVolume == "" {
		return "", ErrSourceVolumeRequired
	}

	// Extract just the snapshot name from the full ZFS snapshot name (dataset@snapname)
	snapshotName := meta.SnapshotName
	if idx := strings.LastIndex(meta.SnapshotName, "@"); idx != -1 {
		snapshotName = meta.SnapshotName[idx+1:]
	}

	if snapshotName == "" {
		return "", ErrSnapshotNameRequired
	}

	// Format: protocol:volume_id@snapshot_name or detached:protocol:volume_id@snapshot_name
	baseID := fmt.Sprintf("%s:%s@%s", meta.Protocol, meta.SourceVolume, snapshotName)
	if meta.Detached {
		return DetachedSnapshotPrefix + baseID, nil
	}
	return baseID, nil
}

// decodeSnapshotID decodes a snapshotID string into snapshot metadata.
// Supports:
// - Detached format: detached:{protocol}:{volume_id}@{snapshot_name}
// - Compact format: {protocol}:{volume_id}@{snapshot_name}.
func decodeSnapshotID(snapshotID string) (*SnapshotMetadata, error) {
	// Check for detached snapshot prefix first
	if strings.HasPrefix(snapshotID, DetachedSnapshotPrefix) {
		// Strip the prefix and decode as compact format
		trimmedID := strings.TrimPrefix(snapshotID, DetachedSnapshotPrefix)
		meta, err := decodeCompactSnapshotID(trimmedID)
		if err != nil {
			return nil, err
		}
		meta.Detached = true
		return meta, nil
	}

	// Decode compact format
	return decodeCompactSnapshotID(snapshotID)
}

// decodeCompactSnapshotID decodes the new compact format: {protocol}:{volume_id}@{snapshot_name}.
func decodeCompactSnapshotID(snapshotID string) (*SnapshotMetadata, error) {
	// Format: protocol:volume_id@snapshot_name
	// First split by ":" to get protocol
	colonIdx := strings.Index(snapshotID, ":")
	if colonIdx == -1 {
		return nil, fmt.Errorf("%w: missing protocol separator", ErrInvalidSnapshotIDFormat)
	}

	protocol := snapshotID[:colonIdx]
	remainder := snapshotID[colonIdx+1:]

	// Validate protocol
	if protocol != ProtocolNFS && protocol != ProtocolNVMeOF && protocol != ProtocolISCSI && protocol != ProtocolSMB {
		return nil, fmt.Errorf("%w: %s", ErrInvalidProtocol, protocol)
	}

	// Split remainder by "@" to get volume_id and snapshot_name
	atIdx := strings.LastIndex(remainder, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("%w: missing snapshot separator", ErrInvalidSnapshotIDFormat)
	}

	volumeID := remainder[:atIdx]
	snapshotName := remainder[atIdx+1:]

	if volumeID == "" {
		return nil, fmt.Errorf("%w: empty volume ID", ErrInvalidSnapshotIDFormat)
	}
	if snapshotName == "" {
		return nil, fmt.Errorf("%w: empty snapshot name", ErrInvalidSnapshotIDFormat)
	}

	// Note: DatasetName and full SnapshotName (with dataset path) cannot be reconstructed
	// from the compact format alone. They will be populated by the caller if needed
	// by looking up the volume in TrueNAS or using StorageClass parameters.
	return &SnapshotMetadata{
		Protocol:     protocol,
		SourceVolume: volumeID,
		SnapshotName: snapshotName, // Just the snapshot name, not full ZFS path
		DatasetName:  "",           // Must be resolved by caller
		Detached:     false,
	}, nil
}

// CreateSnapshot creates a volume snapshot.
// Supports two modes based on VolumeSnapshotClass parameters:
// 1. Regular snapshots (default): COW ZFS snapshots, fast but dependent on source.
// 2. Detached snapshots (detachedSnapshots=true): Full copy via zfs send/receive, survives source deletion.
func (s *ControllerService) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	timer := metrics.NewVolumeOperationTimer("snapshot", "create")
	klog.V(4).Infof("CreateSnapshot called with request: %+v", req)

	// Validate request
	if req.GetName() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Snapshot name is required")
	}

	if req.GetSourceVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Source volume ID is required")
	}

	snapshotName := req.GetName()
	sourceVolumeID := req.GetSourceVolumeId()

	// With plain volume IDs (just the volume name), we need to look up the volume in TrueNAS.
	// We need to find the dataset name and protocol for the source volume.
	params := req.GetParameters()
	pool := params["pool"]
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Determine protocol from parameters (default to NFS)
	protocol := params["protocol"]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	// Check if detached snapshots are requested
	detached := params[DetachedSnapshotsParam] == VolumeContextValueTrue
	detachedParentDataset := params[DetachedSnapshotsParentDatasetParam]

	// Try to find the volume's dataset using property-based lookup (preferred method)
	var datasetName string
	if parentDataset != "" {
		// Use property-based lookup to find the volume by its CSI name
		volumeMeta, err := s.lookupVolumeByCSIName(ctx, parentDataset, sourceVolumeID)
		if err != nil {
			klog.Warningf("Property-based lookup failed for volume %s: %v, falling back to name-based lookup", sourceVolumeID, err)
		} else if volumeMeta != nil {
			datasetName = volumeMeta.DatasetID
			if volumeMeta.Protocol != "" {
				protocol = volumeMeta.Protocol
			}
			klog.V(4).Infof("Found volume %s via property lookup: dataset=%s, protocol=%s", sourceVolumeID, datasetName, protocol)
		}

		// Fallback to name-based lookup if property lookup didn't find the volume
		if datasetName == "" {
			if isDatasetPathVolumeID(sourceVolumeID) {
				datasetName = sourceVolumeID
			} else {
				datasetName = fmt.Sprintf("%s/%s", parentDataset, sourceVolumeID)
			}
			klog.V(4).Infof("Using name-based dataset path for volume %s: %s", sourceVolumeID, datasetName)
		}
	} else {
		// If no parent dataset specified, try to find the volume by searching shares/namespaces/extents
		result := s.discoverVolumeBySearching(ctx, sourceVolumeID)
		if result != nil {
			datasetName = result.datasetName
			protocol = result.protocol
		}
	}

	if datasetName == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.NotFound, "Source volume %s not found", sourceVolumeID)
	}

	// Query source volume capacity for SizeBytes in snapshot response
	var sourceCapacityBytes int64
	dataset, getErr := s.apiClient.GetDatasetWithProperties(ctx, datasetName)
	if getErr == nil && dataset != nil {
		if capProp, ok := dataset.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
			sourceCapacityBytes = tnsapi.StringToInt64(capProp.Value)
		}
		// Fallback: for ZVOLs, use volsize directly
		if sourceCapacityBytes == 0 {
			sourceCapacityBytes = getZvolCapacity(&dataset.Dataset)
		}
	}

	// Route to appropriate snapshot creation method
	if detached {
		return s.createDetachedSnapshot(ctx, timer, snapshotName, sourceVolumeID, datasetName, protocol, pool, detachedParentDataset, sourceCapacityBytes)
	}

	return s.createRegularSnapshot(ctx, timer, snapshotName, sourceVolumeID, datasetName, protocol, sourceCapacityBytes)
}

// createRegularSnapshot creates a traditional COW ZFS snapshot.
func (s *ControllerService) createRegularSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotName, sourceVolumeID, datasetName, protocol string, sizeBytes int64) (*csi.CreateSnapshotResponse, error) {
	klog.Infof("Creating regular snapshot %s for volume %s (dataset: %s, protocol: %s)",
		snapshotName, sourceVolumeID, datasetName, protocol)

	// Check for global uniqueness by querying TrueNAS for any snapshot with this name.
	// CSI spec requires snapshot names to be globally unique across all volumes.
	// ZFS only enforces per-dataset uniqueness, so we must check across all datasets.
	existingSnapshots, err := s.apiClient.QuerySnapshots(ctx, []interface{}{
		[]interface{}{"name", "=", snapshotName},
	})
	if err != nil {
		klog.Warningf("Failed to query existing snapshots: %v", err)
		// Continue anyway - creation will fail if snapshot exists
	} else if len(existingSnapshots) > 0 {
		// Found snapshot(s) with this name - check if it's on our dataset (idempotent) or different (conflict)
		for _, snapshot := range existingSnapshots {
			klog.V(4).Infof("Found existing snapshot with name %s: %s", snapshotName, snapshot.ID)

			// Extract dataset name from snapshot ID (format: dataset@snapname)
			parts := strings.Split(snapshot.ID, "@")
			if len(parts) != 2 {
				klog.Warningf("Invalid snapshot ID format: %s", snapshot.ID)
				continue
			}
			existingDataset := parts[0]

			if existingDataset == datasetName {
				// Snapshot exists on the same dataset - this is idempotent, return existing
				klog.Infof("Snapshot %s already exists on dataset %s (idempotent)", snapshotName, datasetName)

				createdAt := time.Now().Unix() // Use current time as we don't have creation time from API
				snapshotMeta := SnapshotMetadata{
					SnapshotName: snapshot.ID,
					SourceVolume: sourceVolumeID,
					DatasetName:  datasetName,
					Protocol:     protocol,
					CreatedAt:    createdAt,
					Detached:     false,
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
						ReadyToUse:     true, // ZFS snapshots are immediately available
						SizeBytes:      sizeBytes,
					},
				}, nil
			}

			// Snapshot exists on a different dataset - this is a conflict
			timer.ObserveError()
			return nil, status.Errorf(codes.AlreadyExists,
				"snapshot name %q already exists on different volume (dataset: %s vs %s)",
				snapshotName, existingDataset, datasetName)
		}
	}

	// Create snapshot using TrueNAS API
	snapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   datasetName,
		Name:      snapshotName,
		Recursive: false,
	}

	snapshot, err := s.apiClient.CreateSnapshot(ctx, snapshotParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create snapshot: %v", err)
	}

	klog.Infof("Successfully created snapshot: %s", snapshot.ID)

	// Step 4: Set CSI metadata properties on the snapshot
	props := map[string]string{
		tnsapi.PropertyManagedBy:        tnsapi.ManagedByValue,
		tnsapi.PropertySnapshotID:       snapshotName,
		tnsapi.PropertySourceVolumeID:   sourceVolumeID,
		tnsapi.PropertyDetachedSnapshot: VolumeContextValueFalse,
		tnsapi.PropertyProtocol:         protocol,
		tnsapi.PropertyDeleteStrategy:   "delete",
	}
	if err := s.apiClient.SetSnapshotProperties(ctx, snapshot.ID, props, nil); err != nil {
		klog.Warningf("Failed to set CSI properties on snapshot: %v", err)
		// Non-fatal - the snapshot is still usable
	}

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  datasetName,
		Protocol:     protocol,
		CreatedAt:    createdAt,
		Detached:     false,
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
			ReadyToUse:     true, // ZFS snapshots are immediately available
			SizeBytes:      sizeBytes,
		},
	}, nil
}

// DeleteSnapshot deletes a snapshot.
func (s *ControllerService) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	timer := metrics.NewVolumeOperationTimer("snapshot", "delete")
	klog.V(4).Infof("DeleteSnapshot called with request: %+v", req)

	if req.GetSnapshotId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID is required")
	}

	snapshotID := req.GetSnapshotId()
	klog.Infof("Deleting snapshot %s", snapshotID)

	// Decode snapshot metadata
	snapshotMeta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		// If we can't decode the snapshot ID, log a warning but return success
		// per CSI spec (DeleteSnapshot should be idempotent)
		klog.Warningf("Failed to decode snapshot ID %s: %v. Assuming snapshot doesn't exist.", snapshotID, err)
		timer.ObserveSuccess()
		return &csi.DeleteSnapshotResponse{}, nil
	}

	// Handle detached snapshots differently - they are datasets, not ZFS snapshots
	if snapshotMeta.Detached {
		return s.deleteDetachedSnapshot(ctx, timer, snapshotMeta)
	}

	// Regular snapshot deletion
	return s.deleteRegularSnapshot(ctx, timer, snapshotMeta)
}

// deleteRegularSnapshot deletes a traditional COW ZFS snapshot.
func (s *ControllerService) deleteRegularSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotMeta *SnapshotMetadata) (*csi.DeleteSnapshotResponse, error) {
	// Resolve the full ZFS snapshot name if we only have the short name
	// Compact format gives us just the snapshot name, need to find full path
	zfsSnapshotName, err := s.resolveZFSSnapshotName(ctx, snapshotMeta)
	if err != nil {
		// If we can't resolve the snapshot, it might not exist
		klog.Warningf("Failed to resolve ZFS snapshot name: %v. Assuming snapshot doesn't exist.", err)
		timer.ObserveSuccess()
		return &csi.DeleteSnapshotResponse{}, nil
	}

	klog.Infof("Deleting ZFS snapshot: %s", zfsSnapshotName)

	// Delete snapshot using TrueNAS API
	if err := s.apiClient.DeleteSnapshot(ctx, zfsSnapshotName); err != nil {
		// Check if error is because snapshot doesn't exist
		if isNotFoundError(err) {
			klog.Infof("Snapshot %s not found, assuming already deleted", zfsSnapshotName)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete snapshot: %v", err)
	}

	klog.Infof("Successfully deleted snapshot: %s", zfsSnapshotName)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}

// resolveZFSSnapshotName resolves the full ZFS snapshot name (dataset@snapname) from metadata.
// For legacy format, SnapshotName already contains the full path.
// For compact format with new-style volume IDs (containing "/"), we construct the name directly.
// For compact format with old-style volume IDs (plain PVC name), we use a filtered query.
func (s *ControllerService) resolveZFSSnapshotName(ctx context.Context, meta *SnapshotMetadata) (string, error) {
	// If SnapshotName already contains "@", it's the full ZFS path (legacy format)
	if strings.Contains(meta.SnapshotName, "@") {
		return meta.SnapshotName, nil
	}

	snapshotName := meta.SnapshotName
	volumeID := meta.SourceVolume

	// New format: volumeID is full dataset path (contains "/") → construct directly, no query needed
	if strings.Contains(volumeID, "/") {
		return volumeID + "@" + snapshotName, nil
	}

	// Old format: volumeID is plain PVC name → use filtered query by snapshot name
	snapshots, err := s.apiClient.QuerySnapshots(ctx, []interface{}{
		[]interface{}{"name", "=", snapshotName},
	})
	if err != nil {
		return "", fmt.Errorf("failed to query snapshots: %w", err)
	}

	for _, snap := range snapshots {
		if !strings.HasSuffix(snap.ID, "@"+snapshotName) {
			continue
		}
		datasetPath := strings.TrimSuffix(snap.ID, "@"+snapshotName)
		if strings.Contains(datasetPath, volumeID) {
			return snap.ID, nil
		}
	}

	return "", fmt.Errorf("%w: snapshot %s for volume %s", ErrSnapshotNotFoundTrueNAS, snapshotName, volumeID)
}

// volumeDiscoveryResult holds the result of searching for a volume across protocols.
type volumeDiscoveryResult struct {
	datasetName string
	protocol    string
}

// discoverVolumeBySearching searches for a volume by querying NFS shares, NVMe-oF namespaces, and iSCSI extents.
// This is used as a fallback when the parent dataset is not specified.
func (s *ControllerService) discoverVolumeBySearching(ctx context.Context, volumeID string) *volumeDiscoveryResult {
	// Use property-based lookup first (handles both new and legacy volume IDs)
	meta, err := s.lookupVolumeByCSIName(ctx, "", volumeID)
	if err == nil && meta != nil {
		return &volumeDiscoveryResult{datasetName: meta.DatasetName, protocol: meta.Protocol}
	}

	// Fallback for unmigrated volumes: search NFS shares, SMB shares, NVMe-oF namespaces, iSCSI extents
	shares, err := s.apiClient.QueryAllNFSShares(ctx, volumeID)
	if err == nil && len(shares) > 0 {
		for _, share := range shares {
			if strings.HasSuffix(share.Path, "/"+volumeID) {
				datasetID := mountpointToDatasetID(share.Path)
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, datasetID)
				if dsErr == nil && len(datasets) > 0 {
					return &volumeDiscoveryResult{datasetName: datasets[0].Name, protocol: ProtocolNFS}
				}
			}
		}
	}

	smbShares, err := s.apiClient.QueryAllSMBShares(ctx, volumeID)
	if err == nil && len(smbShares) > 0 {
		for _, share := range smbShares {
			if strings.HasSuffix(share.Path, "/"+volumeID) {
				datasetID := mountpointToDatasetID(share.Path)
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, datasetID)
				if dsErr == nil && len(datasets) > 0 {
					return &volumeDiscoveryResult{datasetName: datasets[0].Name, protocol: ProtocolSMB}
				}
			}
		}
	}

	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err == nil {
		for _, ns := range namespaces {
			devicePath := ns.GetDevice()
			if strings.Contains(devicePath, volumeID) {
				return &volumeDiscoveryResult{
					datasetName: strings.TrimPrefix(devicePath, "zvol/"),
					protocol:    ProtocolNVMeOF,
				}
			}
		}
	}

	extents, err := s.apiClient.QueryISCSIExtents(ctx, nil)
	if err == nil {
		for _, extent := range extents {
			if strings.Contains(extent.Disk, volumeID) {
				return &volumeDiscoveryResult{
					datasetName: strings.TrimPrefix(extent.Disk, "zvol/"),
					protocol:    ProtocolISCSI,
				}
			}
		}
	}

	return nil
}

// isNotFoundError checks if an error indicates a resource was not found.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains common "not found" indicators
	errStr := err.Error()
	return containsAny(errStr, []string{"not found", "does not exist", "ENOENT"})
}

// containsAny checks if a string contains any of the given substrings.
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}
