package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	if protocol != ProtocolNFS && protocol != ProtocolNVMeOF && protocol != ProtocolISCSI {
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

	// Route to appropriate snapshot creation method
	if detached {
		return s.createDetachedSnapshot(ctx, timer, snapshotName, sourceVolumeID, datasetName, protocol, pool, detachedParentDataset)
	}

	return s.createRegularSnapshot(ctx, timer, snapshotName, sourceVolumeID, datasetName, protocol)
}

// createRegularSnapshot creates a traditional COW ZFS snapshot.
func (s *ControllerService) createRegularSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotName, sourceVolumeID, datasetName, protocol string) (*csi.CreateSnapshotResponse, error) {
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
		},
	}, nil
}

// createDetachedSnapshot creates a detached snapshot using zfs send/receive via TrueNAS replication API.
// Detached snapshots are stored as full dataset copies, independent of the source volume.
// They survive deletion of the source volume, making them suitable for backup/DR scenarios.
func (s *ControllerService) createDetachedSnapshot(ctx context.Context, timer *metrics.OperationTimer, snapshotName, sourceVolumeID, sourceDataset, protocol, pool, detachedParentDataset string) (*csi.CreateSnapshotResponse, error) {
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
		tnsapi.PropertyDeleteStrategy:   "delete",
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
		Type: "FILESYSTEM",
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

	// Snapshot exists - return it with the metadata we decoded
	// (which includes protocol, source volume, etc.)
	entry := &csi.ListSnapshotsResponse_Entry{
		Snapshot: &csi.Snapshot{
			SnapshotId:     req.GetSnapshotId(), // Return the same ID we were queried with
			SourceVolumeId: snapshotMeta.SourceVolume,
			CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
			ReadyToUse:     true,
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

	// Snapshot exists - return it
	entry := &csi.ListSnapshotsResponse_Entry{
		Snapshot: &csi.Snapshot{
			SnapshotId:     req.GetSnapshotId(), // Return the same ID we were queried with
			SourceVolumeId: resolvedMeta.SourceVolume,
			CreationTime:   timestamppb.New(time.Now()), // We don't store creation time in properties
			ReadyToUse:     true,
		},
	}

	return &csi.ListSnapshotsResponse{
		Entries: []*csi.ListSnapshotsResponse_Entry{entry},
	}, nil
}

// listSnapshotsBySourceVolume handles listing snapshots for a specific source volume.
func (s *ControllerService) listSnapshotsBySourceVolume(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()

	// Determine dataset name and protocol for the source volume
	var datasetName string
	var protocol string
	if isDatasetPathVolumeID(sourceVolumeID) {
		// New format: volume ID is the dataset path, use directly (O(1))
		datasetName = sourceVolumeID
		// Look up protocol from dataset properties
		dataset, err := s.apiClient.GetDatasetWithProperties(ctx, sourceVolumeID)
		if err == nil && dataset != nil {
			if prop, ok := dataset.UserProperties[tnsapi.PropertyProtocol]; ok {
				protocol = prop.Value
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
		[]interface{}{"dataset", "=", datasetName},
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
		volumeID string
		protocol string
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
		managedMeta[ds.ID] = datasetMeta{volumeID: volumeID, protocol: protocol}
	}

	// Query snapshots per managed dataset (each query is small and filtered)
	var allSnapshots []tnsapi.Snapshot
	for datasetID := range managedMeta {
		snaps, queryErr := s.apiClient.QuerySnapshots(ctx, []interface{}{
			[]interface{}{"dataset", "=", datasetID},
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
	// - default: Standard COW clone (clone depends on snapshot)
	detachedMode := params[DetachedVolumesFromSnapshotsParam] == VolumeContextValueTrue
	promotedMode := params[PromotedVolumesFromSnapshotsParam] == VolumeContextValueTrue

	if detachedMode && promotedMode {
		klog.Warningf("Both detachedVolumesFromSnapshots and promotedVolumesFromSnapshots are set; using detached mode")
		promotedMode = false
	}

	// Clone/restore the snapshot based on source type and clone mode:
	//
	// Source types:
	// - Detached snapshot (stored as dataset) -> always use executeDetachedSnapshotRestore
	// - Regular ZFS snapshot -> depends on clone mode
	//
	// Clone modes for regular snapshots:
	// 1. detachedVolumesFromSnapshots=true -> executeDetachedVolumeClone (send/receive, truly independent)
	// 2. promotedVolumesFromSnapshots=true -> executePromotedSnapshotClone (clone+promote, reversed dependency)
	// 3. default -> executeSnapshotClone (COW clone, normal dependency)

	// Determine clone mode: detachedSnapshotRestore, detached, promoted, or cow (default)
	type cloneMode int
	const (
		cloneModeDetachedSnapshotRestore cloneMode = iota
		cloneModeDetached
		cloneModePromoted
		cloneModeCOW
	)

	var mode cloneMode
	switch {
	case snapshotMeta.Detached:
		mode = cloneModeDetachedSnapshotRestore
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
		// We need to create a temp snapshot of the dataset, then clone from it
		klog.Infof("Restoring volume %s from detached snapshot dataset %s", req.GetName(), snapshotMeta.DatasetName)
		clonedDataset, cloneErr = s.executeDetachedSnapshotRestore(ctx, snapshotMeta, cloneParams)
	case cloneModeDetached:
		// User wants truly independent copy via send/receive
		klog.Infof("Creating detached (send/receive) volume %s from snapshot (truly independent)", req.GetName())
		clonedDataset, cloneErr = s.executeDetachedVolumeClone(ctx, snapshotMeta, cloneParams)
	case cloneModePromoted:
		// User wants clone+promote (reversed dependency, allows snapshot deletion)
		klog.Infof("Creating promoted clone for volume %s from snapshot (reversed dependency)", req.GetName())
		clonedDataset, cloneErr = s.executePromotedSnapshotClone(ctx, snapshotMeta, cloneParams)
	case cloneModeCOW:
		// Default: Standard COW clone (clone depends on snapshot)
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
		// Restore from detached snapshot creates a COW clone from a temp snapshot
		cloneInfoData.Mode = tnsapi.CloneModeCOW
		// The origin is the temp snapshot on the detached snapshot dataset
		cloneInfoData.OriginSnapshot = snapshotMeta.DatasetName + "@csi-restore-for-" + req.GetName()
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

// cloneParameters holds validated parameters for snapshot cloning.
type cloneParameters struct {
	pool           string
	parentDataset  string
	newVolumeName  string
	newDatasetName string
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

	return &cloneParameters{
		pool:           pool,
		parentDataset:  parentDataset,
		newVolumeName:  newVolumeName,
		newDatasetName: newDatasetName,
	}, nil
}

// executeSnapshotClone performs the actual snapshot clone operation.
func (s *ControllerService) executeSnapshotClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Cloning snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot: snapshotMeta.SnapshotName,
		Dataset:  params.newDatasetName,
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
		Snapshot: snapshotMeta.SnapshotName,
		Dataset:  params.newDatasetName,
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
// IMPORTANT: We do NOT promote the cloned volume. This maintains the correct
// dependency direction:
// - Restored volume depends on detached snapshot (via the temp snapshot)
// - Can delete restored volumes freely (they're the dependents, not origins)
// - Can't delete detached snapshot while restored volumes exist (expected for backups)
//
// If we promoted, the dependency would be REVERSED - the detached snapshot would
// depend on the restored volume's snapshot, preventing deletion of restored volumes.
//
// The temp snapshot on the detached snapshot dataset is kept because the restored
// volume depends on it. It will be cleaned up when the restored volume is deleted.
func (s *ControllerService) executeDetachedSnapshotRestore(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Restoring volume from detached snapshot dataset %s to %s", snapshotMeta.DatasetName, params.newDatasetName)

	// Step 1: Create a temporary ZFS snapshot of the detached snapshot dataset
	// This snapshot will persist because the cloned volume depends on it
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
	// The clone will depend on the snapshot (correct dependency direction)
	klog.V(4).Infof("Cloning snapshot %s to %s", tempSnapshotFullName, params.newDatasetName)

	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot: tempSnapshotFullName,
		Dataset:  params.newDatasetName,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneParams)
	if err != nil {
		klog.Errorf("Failed to clone snapshot: %v", err)
		// Don't delete the temp snapshot - it might be used by other restores
		// or might be needed for a retry
		return nil, status.Errorf(codes.Internal, "Failed to clone detached snapshot: %v", err)
	}

	klog.Infof("Successfully restored volume from detached snapshot: %s -> %s (clone depends on %s)",
		snapshotMeta.DatasetName, clonedDataset.Name, tempSnapshotFullName)

	// NOTE: We intentionally do NOT promote the clone. This keeps the dependency
	// direction correct: restored volume depends on detached snapshot.
	// The temp snapshot will be cleaned up when the restored volume is deleted
	// (ZFS will allow deletion of the snapshot once no clones depend on it).

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

// volumeDiscoveryResult holds the result of searching for a volume across protocols.
type volumeDiscoveryResult struct {
	datasetName string
	protocol    string
}

// discoverVolumeBySearching searches for a volume by querying NFS shares, NVMe-oF namespaces, and iSCSI extents.
// This is used as a fallback when the parent dataset is not specified.
func (s *ControllerService) discoverVolumeBySearching(ctx context.Context, volumeID string) *volumeDiscoveryResult {
	// First try NFS shares
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

	// Try NVMe-oF namespaces
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

	// Try iSCSI extents
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
