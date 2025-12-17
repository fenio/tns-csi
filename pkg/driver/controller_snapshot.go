package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// Static errors for snapshot operations.
var (
	ErrSnapshotNameExists      = errors.New("snapshot name already exists for different dataset")
	ErrProtocolRequired        = errors.New("protocol is required for snapshot ID encoding")
	ErrSourceVolumeRequired    = errors.New("source volume is required for snapshot ID encoding")
	ErrSnapshotNameRequired    = errors.New("snapshot name is required for snapshot ID encoding")
	ErrInvalidSnapshotIDFormat = errors.New("invalid compact snapshot ID format")
	ErrInvalidProtocol         = errors.New("invalid protocol in snapshot ID")
	ErrSnapshotNotFoundTrueNAS = errors.New("snapshot not found in TrueNAS")
)

// SnapshotRegistry maintains a global registry of snapshot names to enforce
// CSI's requirement that snapshot names be globally unique.
// This bridges the gap between CSI (global uniqueness) and ZFS (per-dataset uniqueness).
type SnapshotRegistry struct {
	snapshots map[string]string // snapshot name -> dataset name
	mu        sync.RWMutex
}

// NewSnapshotRegistry creates a new snapshot registry.
func NewSnapshotRegistry() *SnapshotRegistry {
	return &SnapshotRegistry{
		snapshots: make(map[string]string),
	}
}

// Register attempts to register a snapshot name with its dataset.
// Returns an error if the snapshot name already exists with a different dataset.
func (r *SnapshotRegistry) Register(snapshotName, datasetName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existingDataset, exists := r.snapshots[snapshotName]; exists {
		if existingDataset != datasetName {
			return fmt.Errorf("%w: snapshot name %q already exists for dataset %q",
				ErrSnapshotNameExists, snapshotName, existingDataset)
		}
		// Already registered with same dataset - idempotent
		return nil
	}

	r.snapshots[snapshotName] = datasetName
	return nil
}

// Unregister removes a snapshot name from the registry.
func (r *SnapshotRegistry) Unregister(snapshotName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.snapshots, snapshotName)
}

// Dataset returns the dataset name for a registered snapshot, or empty string if not found.
func (r *SnapshotRegistry) Dataset(snapshotName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshots[snapshotName]
}

// SnapshotMetadata contains information needed to manage a snapshot.
type SnapshotMetadata struct {
	SnapshotName string `json:"snapshotName"` // ZFS snapshot name (dataset@snapshot)
	SourceVolume string `json:"sourceVolume"` // Source volume ID
	DatasetName  string `json:"datasetName"`  // Parent dataset name
	Protocol     string `json:"protocol"`     // Protocol (nfs, nvmeof)
	CreatedAt    int64  `json:"-"`            // Creation timestamp (Unix epoch) - excluded from ID encoding
}

// Compact snapshot ID format: {protocol}:{volume_id}@{snapshot_name}.
// Example: "nfs:pvc-abc123@snap-xyz789" (~65 bytes vs 300+ for base64 JSON).
// This format is CSI-compliant (under 128 bytes) and easy to parse.
//
// The full ZFS dataset path can be reconstructed from:
// - parentDataset (from StorageClass parameters) + volumeID.
// - Format: {parentDataset}/{volumeID}@{snapshotName}.

// encodeSnapshotID encodes snapshot metadata into a compact snapshotID string.
// Format: {protocol}:{volume_id}@{snapshot_name}.
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

	// Format: protocol:volume_id@snapshot_name
	return fmt.Sprintf("%s:%s@%s", meta.Protocol, meta.SourceVolume, snapshotName), nil
}

// decodeSnapshotID decodes a snapshotID string into snapshot metadata.
// Supports both:
// - New compact format: {protocol}:{volume_id}@{snapshot_name}.
// - Legacy base64-encoded JSON format (for backward compatibility).
func decodeSnapshotID(snapshotID string) (*SnapshotMetadata, error) {
	// Try compact format first (has colon before @)
	if meta, err := decodeCompactSnapshotID(snapshotID); err == nil {
		return meta, nil
	}

	// Fall back to legacy base64 JSON format
	return decodeLegacySnapshotID(snapshotID)
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
	if protocol != ProtocolNFS && protocol != ProtocolNVMeOF {
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
	}, nil
}

// decodeLegacySnapshotID decodes the old base64-encoded JSON format for backward compatibility.
func decodeLegacySnapshotID(snapshotID string) (*SnapshotMetadata, error) {
	data, err := base64.RawURLEncoding.DecodeString(snapshotID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode snapshot ID: %w", err)
	}

	var meta SnapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal snapshot metadata: %w", err)
	}

	return &meta, nil
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

	// Try to find the volume's dataset
	var datasetName string
	if parentDataset != "" {
		datasetName = fmt.Sprintf("%s/%s", parentDataset, sourceVolumeID)
	} else {
		// If no parent dataset specified, try to find the volume
		// First try NFS shares
		shares, err := s.apiClient.QueryAllNFSShares(ctx, sourceVolumeID)
		if err == nil && len(shares) > 0 {
			for _, share := range shares {
				if strings.HasSuffix(share.Path, "/"+sourceVolumeID) {
					// Convert mountpoint to dataset ID (strip /mnt/ prefix)
					datasetID := mountpointToDatasetID(share.Path)
					datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, datasetID)
					if dsErr == nil && len(datasets) > 0 {
						datasetName = datasets[0].Name
						protocol = ProtocolNFS
						break
					}
				}
			}
		}
		// If not found as NFS, try NVMe-oF namespaces
		if datasetName == "" {
			namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
			if err == nil {
				for _, ns := range namespaces {
					devicePath := ns.GetDevice()
					if strings.Contains(devicePath, sourceVolumeID) {
						datasetName = strings.TrimPrefix(devicePath, "zvol/")
						protocol = ProtocolNVMeOF
						break
					}
				}
			}
		}
	}

	if datasetName == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.NotFound, "Source volume %s not found", sourceVolumeID)
	}

	klog.Infof("Creating snapshot %s for volume %s (dataset: %s, protocol: %s)",
		snapshotName, sourceVolumeID, datasetName, protocol)

	// CRITICAL: Check snapshot name registry FIRST to enforce global uniqueness
	// This is required by CSI spec - snapshot names must be globally unique across all volumes
	if regErr := s.snapshotRegistry.Register(snapshotName, datasetName); regErr != nil {
		// Snapshot name already exists for a different dataset
		timer.ObserveError()
		return nil, status.Errorf(codes.AlreadyExists,
			"Snapshot name %q is already in use for a different volume: %v", snapshotName, regErr)
	}

	// Check if snapshot already exists (idempotency)
	existingSnapshots, err := s.apiClient.QuerySnapshots(ctx, []interface{}{
		[]interface{}{"id", "=", fmt.Sprintf("%s@%s", datasetName, snapshotName)},
	})
	if err != nil {
		klog.Warningf("Failed to query existing snapshots: %v", err)
	} else if len(existingSnapshots) > 0 {
		// Snapshot already exists
		klog.Infof("Snapshot %s already exists, verifying source volume", snapshotName)
		snapshot := existingSnapshots[0]

		// Extract dataset name from snapshot ID (format: dataset@snapname)
		parts := strings.Split(snapshot.ID, "@")
		if len(parts) != 2 {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Invalid snapshot ID format: %s", snapshot.ID)
		}
		existingDataset := parts[0]

		// Verify the existing snapshot is for the same source volume
		// by comparing dataset names
		if existingDataset != datasetName {
			timer.ObserveError()
			return nil, status.Errorf(codes.AlreadyExists,
				"snapshot %s already exists but for different source volume (dataset: %s vs %s)",
				snapshotName, existingDataset, datasetName)
		}

		// Create snapshot metadata
		createdAt := time.Now().Unix() // Use current time as we don't have creation time from API
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			SourceVolume: sourceVolumeID,
			DatasetName:  datasetName,
			Protocol:     protocol,
			CreatedAt:    createdAt,
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

	// Create snapshot using TrueNAS API
	snapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   datasetName,
		Name:      snapshotName,
		Recursive: false,
	}

	snapshot, err := s.apiClient.CreateSnapshot(ctx, snapshotParams)
	if err != nil {
		// Unregister the snapshot name since creation failed
		s.snapshotRegistry.Unregister(snapshotName)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create snapshot: %v", err)
	}

	klog.Infof("Successfully created snapshot: %s", snapshot.ID)

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  datasetName,
		Protocol:     protocol,
		CreatedAt:    createdAt,
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
			// Unregister from registry since it doesn't exist anymore
			s.snapshotRegistry.Unregister(snapshotMeta.SnapshotName)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete snapshot: %v", err)
	}

	// Unregister the snapshot name from the registry
	s.snapshotRegistry.Unregister(snapshotMeta.SnapshotName)
	klog.V(4).Infof("Unregistered snapshot name %q from registry", snapshotMeta.SnapshotName)

	klog.Infof("Successfully deleted snapshot: %s", zfsSnapshotName)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}

// resolveZFSSnapshotName resolves the full ZFS snapshot name (dataset@snapname) from metadata.
// For legacy format, SnapshotName already contains the full path.
// For compact format, we need to look up the volume to get the dataset path.
func (s *ControllerService) resolveZFSSnapshotName(ctx context.Context, meta *SnapshotMetadata) (string, error) {
	// If SnapshotName already contains "@", it's the full ZFS path (legacy format)
	if strings.Contains(meta.SnapshotName, "@") {
		return meta.SnapshotName, nil
	}

	// Compact format: SnapshotName is just the snapshot name, need to find dataset
	snapshotName := meta.SnapshotName
	volumeID := meta.SourceVolume

	// Query TrueNAS to find snapshots matching this name
	// We search for snapshots ending with @{snapshotName}
	snapshots, err := s.apiClient.QuerySnapshots(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to query snapshots: %w", err)
	}

	// Look for a snapshot that matches our criteria:
	// 1. Ends with @{snapshotName}
	// 2. Dataset path contains the volumeID
	for _, snap := range snapshots {
		// ZFS snapshot ID format: dataset@snapname
		if !strings.HasSuffix(snap.ID, "@"+snapshotName) {
			continue
		}

		// Check if the dataset contains our volume ID
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

	// Resolve the full ZFS snapshot name if we only have the short name
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

// listSnapshotsBySourceVolume handles listing snapshots for a specific source volume.
func (s *ControllerService) listSnapshotsBySourceVolume(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()

	// With plain volume IDs, we need to look up the volume in TrueNAS
	// Try to find the dataset name by searching for NFS shares or NVMe-oF namespaces
	var datasetName string
	var protocol string

	// First try NFS shares
	shares, err := s.apiClient.QueryAllNFSShares(ctx, sourceVolumeID)
	if err == nil && len(shares) > 0 {
		for _, share := range shares {
			if strings.HasSuffix(share.Path, "/"+sourceVolumeID) {
				// Convert mountpoint to dataset ID (strip /mnt/ prefix)
				datasetID := mountpointToDatasetID(share.Path)
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, datasetID)
				if dsErr == nil && len(datasets) > 0 {
					datasetName = datasets[0].Name
					protocol = ProtocolNFS
					break
				}
			}
		}
	}

	// If not found as NFS, try NVMe-oF namespaces
	if datasetName == "" {
		namespaces, nsErr := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
		if nsErr == nil {
			for _, ns := range namespaces {
				if strings.Contains(ns.GetDevice(), sourceVolumeID) {
					datasetName = strings.TrimPrefix(ns.GetDevice(), "zvol/")
					protocol = ProtocolNVMeOF
					break
				}
			}
		}
	}

	if datasetName == "" {
		// If we can't find the volume, return empty list
		klog.V(4).Infof("Source volume %q not found in TrueNAS - returning empty list", sourceVolumeID)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
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
func (s *ControllerService) listAllSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// Query all snapshots
	snapshots, err := s.apiClient.QuerySnapshots(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query snapshots: %v", err)
	}

	klog.V(4).Infof("Found %d total snapshots", len(snapshots))

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
	// Note: We try to infer protocol and source volume from ZFS dataset info
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapshot := snapshots[i]

		// Extract snapshot name and infer metadata from ZFS path
		// ZFS snapshot ID format: dataset@snapname or zvol/dataset@snapname
		snapshotMeta := s.inferSnapshotMetadataFromZFS(snapshot)

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v - skipping", snapshot.ID, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: snapshotMeta.SourceVolume,
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

// inferSnapshotMetadataFromZFS infers snapshot metadata from ZFS snapshot info.
// This is used when listing all snapshots where we don't have explicit metadata.
func (s *ControllerService) inferSnapshotMetadataFromZFS(snapshot tnsapi.Snapshot) SnapshotMetadata {
	// ZFS snapshot ID format: dataset@snapname
	// For zvols: pool/path/to/volume@snapname
	// For filesystems: pool/path/to/dataset@snapname
	datasetName := snapshot.Dataset

	// Infer protocol from dataset path
	// NVMe-oF volumes are typically zvols (visible in /dev/zvol/...)
	// NFS volumes are filesystems
	// Without querying TrueNAS, we assume NFS as the default
	protocol := ProtocolNFS

	// Extract volume ID from dataset name (last component)
	// Format: pool/parent/volumeID -> volumeID
	volumeID := datasetName
	if idx := strings.LastIndex(datasetName, "/"); idx != -1 {
		volumeID = datasetName[idx+1:]
	}

	// Extract snapshot name from full snapshot ID
	snapshotName := ""
	if idx := strings.LastIndex(snapshot.ID, "@"); idx != -1 {
		snapshotName = snapshot.ID[idx+1:]
	}

	return SnapshotMetadata{
		SnapshotName: snapshotName,
		SourceVolume: volumeID,
		DatasetName:  datasetName,
		Protocol:     protocol,
		CreatedAt:    time.Now().Unix(),
	}
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

	// Resolve the full ZFS snapshot name and dataset info if using compact format
	if resolveErr := s.resolveSnapshotMetadata(ctx, snapshotMeta); resolveErr != nil {
		klog.Warningf("Failed to resolve snapshot metadata: %v. Treating as not found.", resolveErr)
		return nil, status.Errorf(codes.NotFound, "Snapshot not found: %s", snapshotID)
	}

	// Validate and extract clone parameters
	cloneParams, validateErr := s.validateCloneParameters(req, snapshotMeta)
	if validateErr != nil {
		return nil, validateErr
	}

	// Check if detached clone is requested
	// A detached clone is independent from the source snapshot (promoted)
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}
	detached := params["detached"] == "true"

	// Clone the snapshot (detached or regular)
	var clonedDataset *tnsapi.Dataset
	var cloneErr error
	if detached {
		klog.Infof("Creating detached (promoted) clone for volume %s", req.GetName())
		clonedDataset, cloneErr = s.executeDetachedSnapshotClone(ctx, snapshotMeta, cloneParams)
	} else {
		clonedDataset, cloneErr = s.executeSnapshotClone(ctx, snapshotMeta, cloneParams)
	}
	if cloneErr != nil {
		return nil, cloneErr
	}

	// Wait for ZFS metadata sync for NVMe-oF volumes
	s.waitForZFSSyncIfNVMeOF(snapshotMeta.Protocol)

	// Get server and subsystemNQN parameters
	server, subsystemNQN, err := s.getVolumeParametersForSnapshot(ctx, params, snapshotMeta, clonedDataset)
	if err != nil {
		return nil, err
	}

	// Route to protocol-specific volume setup
	return s.setupVolumeFromClone(ctx, req, clonedDataset, snapshotMeta.Protocol, server, subsystemNQN, snapshotID)
}

// resolveSnapshotMetadata resolves missing metadata fields for compact format snapshots.
// For legacy format, the metadata is already complete.
// For compact format, we need to look up the full ZFS snapshot name and dataset info.
func (s *ControllerService) resolveSnapshotMetadata(ctx context.Context, meta *SnapshotMetadata) error {
	// If SnapshotName already contains "@", it's the full ZFS path (legacy format)
	// and DatasetName should also be populated
	if strings.Contains(meta.SnapshotName, "@") && meta.DatasetName != "" {
		return nil
	}

	// Compact format: need to resolve full paths
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

// cloneParameters holds validated parameters for snapshot cloning.
type cloneParameters struct {
	pool           string
	parentDataset  string
	newVolumeName  string
	newDatasetName string
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

// executeDetachedSnapshotClone performs a detached clone operation.
// A detached clone is independent from its parent snapshot - it can exist even after
// the original snapshot is deleted. This is achieved by:
// 1. Creating a temporary snapshot of the source volume
// 2. Cloning the snapshot to a new dataset
// 3. Promoting the clone to break the parent-child relationship
// 4. Deleting the temporary snapshot
//
// This is useful when you want to create a completely independent copy of data
// from a snapshot, without maintaining a dependency on the original snapshot.
func (s *ControllerService) executeDetachedSnapshotClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Creating detached clone from snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	// Step 1: Clone the snapshot to the new dataset
	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot: snapshotMeta.SnapshotName,
		Dataset:  params.newDatasetName,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneParams)
	if err != nil {
		klog.Errorf("Failed to clone snapshot for detached clone: %v", err)
		s.cleanupPartialClone(ctx, params.newDatasetName)
		return nil, status.Errorf(codes.Internal, "Failed to clone snapshot: %v", err)
	}

	klog.Infof("Clone created, now promoting dataset to make it independent: %s", clonedDataset.Name)

	// Step 2: Promote the clone to break the parent-child relationship
	// This makes the clone independent - it no longer depends on the source snapshot
	if err := s.apiClient.PromoteDataset(ctx, clonedDataset.Name); err != nil {
		klog.Errorf("Failed to promote cloned dataset %s: %v. Cleaning up...", clonedDataset.Name, err)
		// Cleanup the clone since promotion failed
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.Name); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset after promotion failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to promote cloned dataset: %v", err)
	}

	klog.Infof("Successfully created detached clone: %s (independent from source snapshot)", clonedDataset.Name)
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
func (s *ControllerService) setupVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, clonedDataset *tnsapi.Dataset, protocol, server, subsystemNQN, snapshotID string) (*csi.CreateVolumeResponse, error) {
	switch protocol {
	case ProtocolNFS:
		return s.setupNFSVolumeFromClone(ctx, req, clonedDataset, server, snapshotID)
	case ProtocolNVMeOF:
		return s.setupNVMeOFVolumeFromCloneWithValidation(ctx, req, clonedDataset, server, subsystemNQN, snapshotID)
	default:
		return s.handleUnknownProtocol(ctx, clonedDataset, protocol)
	}
}

// setupNVMeOFVolumeFromCloneWithValidation validates subsystemNQN and sets up NVMe-oF volume.
func (s *ControllerService) setupNVMeOFVolumeFromCloneWithValidation(ctx context.Context, req *csi.CreateVolumeRequest, clonedDataset *tnsapi.Dataset, server, subsystemNQN, snapshotID string) (*csi.CreateVolumeResponse, error) {
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
	return s.setupNVMeOFVolumeFromClone(ctx, req, clonedDataset, server, subsystemNQN, snapshotID)
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

	klog.V(4).Infof("Server or subsystemNQN not in parameters, extracting from source volume: %s", snapshotMeta.SourceVolume)

	// With plain volume IDs, we need to look up the source volume in TrueNAS
	// to find the server and NQN information.
	sourceVolumeID := snapshotMeta.SourceVolume

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

	// For NVMe-oF, try to find subsystemNQN from the source volume
	if subsystemNQN == "" && snapshotMeta.Protocol == ProtocolNVMeOF {
		// Try to find the NVMe-oF namespace for the source volume
		namespaces, nsErr := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
		if nsErr != nil {
			klog.Warningf("Failed to query NVMe-oF namespaces to find subsystemNQN: %v", nsErr)
		} else {
			for _, ns := range namespaces {
				if strings.Contains(ns.GetDevice(), sourceVolumeID) {
					// Found the namespace - the subsystem NQN is directly in the nested subsys object
					subsystemNQN = ns.GetSubsystemNQN()
					if subsystemNQN != "" {
						klog.V(4).Infof("Found subsystemNQN %s directly from source volume namespace", subsystemNQN)
					}
					break
				}
			}
		}
		if subsystemNQN == "" {
			// subsystemNQN is required for NVMe-oF
			klog.Errorf("subsystemNQN not found for NVMe-oF source volume %s, cleaning up", sourceVolumeID)
			if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
				klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
			}
			return "", "", status.Error(codes.InvalidArgument,
				"subsystemNQN parameter is required in StorageClass for NVMe-oF snapshot restore, or source volume must still exist in TrueNAS")
		}
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
