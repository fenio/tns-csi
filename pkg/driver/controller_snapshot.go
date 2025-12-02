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
	ErrSnapshotNameExists = errors.New("snapshot name already exists for different dataset")
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
	Detached     bool   `json:"detached"`     // Whether this is a detached snapshot (created via replication)
	CreatedAt    int64  `json:"-"`            // Creation timestamp (Unix epoch) - excluded from ID encoding
}

// encodeSnapshotID encodes snapshot metadata into a snapshotID string.
func encodeSnapshotID(meta SnapshotMetadata) (string, error) {
	data, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("failed to marshal snapshot metadata: %w", err)
	}
	// Use base64 URL-safe encoding (no padding) to create a valid snapshotID
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return encoded, nil
}

// decodeSnapshotID decodes a snapshotID string into snapshot metadata.
func decodeSnapshotID(snapshotID string) (*SnapshotMetadata, error) {
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

// Default timeout for detached snapshot operations (zfs send/receive can take a while for large volumes).
const detachedSnapshotTimeout = 30 * time.Minute

// snapshotParameters holds parsed VolumeSnapshotClass parameters.
type snapshotParameters struct {
	pool                  string
	parentDataset         string
	protocol              string
	snapshotParentDataset string // Parent dataset for storing detached snapshots
	detachedSnapshots     bool   // Create detached (independent) snapshot via zfs send/receive
}

// parseSnapshotParameters extracts and validates parameters from VolumeSnapshotClass.
func parseSnapshotParameters(params map[string]string) *snapshotParameters {
	pool := params["pool"]
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	protocol := params["protocol"]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	// Check if detached snapshots are enabled
	// This creates independent snapshot datasets using zfs send/receive instead of ZFS native snapshots
	detachedSnapshots := params["detachedSnapshots"] == VolumeContextValueTrue

	// Get snapshot parent dataset for detached snapshots
	// Default: {parentDataset}/snapshots
	snapshotParentDataset := params["snapshotParentDataset"]
	if snapshotParentDataset == "" && detachedSnapshots {
		snapshotParentDataset = parentDataset + "/snapshots"
	}

	return &snapshotParameters{
		pool:                  pool,
		parentDataset:         parentDataset,
		protocol:              protocol,
		detachedSnapshots:     detachedSnapshots,
		snapshotParentDataset: snapshotParentDataset,
	}
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

	// Parse VolumeSnapshotClass parameters
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}
	snapParams := parseSnapshotParameters(params)

	// Try to find the volume's dataset
	var datasetName string
	if snapParams.parentDataset != "" {
		datasetName = fmt.Sprintf("%s/%s", snapParams.parentDataset, sourceVolumeID)
	} else {
		// If no parent dataset specified, try to find the volume
		// First try NFS shares
		shares, err := s.apiClient.QueryAllNFSShares(ctx, sourceVolumeID)
		if err == nil && len(shares) > 0 {
			for _, share := range shares {
				if strings.HasSuffix(share.Path, "/"+sourceVolumeID) {
					datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, share.Path)
					if dsErr == nil && len(datasets) > 0 {
						datasetName = datasets[0].Name
						snapParams.protocol = ProtocolNFS
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
					if strings.Contains(ns.Device, sourceVolumeID) {
						datasetName = strings.TrimPrefix(ns.Device, "zvol/")
						snapParams.protocol = ProtocolNVMeOF
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

	klog.Infof("Creating snapshot %s for volume %s (dataset: %s, protocol: %s, detached: %v)",
		snapshotName, sourceVolumeID, datasetName, snapParams.protocol, snapParams.detachedSnapshots)

	// CRITICAL: Check snapshot name registry FIRST to enforce global uniqueness
	// This is required by CSI spec - snapshot names must be globally unique across all volumes
	if regErr := s.snapshotRegistry.Register(snapshotName, datasetName); regErr != nil {
		// Snapshot name already exists for a different dataset
		timer.ObserveError()
		return nil, status.Errorf(codes.AlreadyExists,
			"Snapshot name %q is already in use for a different volume: %v", snapshotName, regErr)
	}

	// Route to appropriate snapshot creation method
	if snapParams.detachedSnapshots {
		return s.createDetachedSnapshot(ctx, datasetName, snapshotName, sourceVolumeID, snapParams, timer)
	}
	return s.createNativeSnapshot(ctx, datasetName, snapshotName, sourceVolumeID, snapParams, timer)
}

// createNativeSnapshot creates a standard ZFS snapshot (dataset@snapshot).
// The snapshot maintains a parent-child relationship with the source dataset.
func (s *ControllerService) createNativeSnapshot(
	ctx context.Context,
	datasetName, snapshotName, sourceVolumeID string,
	snapParams *snapshotParameters,
	timer *metrics.OperationTimer,
) (*csi.CreateSnapshotResponse, error) {
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
			Protocol:     snapParams.protocol,
			Detached:     false,
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

	klog.Infof("Successfully created native snapshot: %s", snapshot.ID)

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  datasetName,
		Protocol:     snapParams.protocol,
		Detached:     false,
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

// createDetachedSnapshot creates a detached snapshot as an independent dataset.
// The snapshot is stored at {snapshotParentDataset}/{sourceVolumeID}/{snapshotName}
// and can be deleted independently of the source volume.
//
// Process:
// 1. Create a temporary ZFS snapshot on the source dataset
// 2. Use zfs send/receive to replicate to snapshot storage location
// 3. Delete the temporary snapshot from both source and target
// 4. The resulting dataset is a point-in-time copy, fully independent.
func (s *ControllerService) createDetachedSnapshot(
	ctx context.Context,
	datasetName, snapshotName, sourceVolumeID string,
	snapParams *snapshotParameters,
	timer *metrics.OperationTimer,
) (*csi.CreateSnapshotResponse, error) {
	// Detached snapshot storage path: {snapshotParentDataset}/{sourceVolumeID}/{snapshotName}
	// This structure allows:
	// - Easy listing of all snapshots for a volume
	// - Independent deletion of source volume
	// - Clear organization of snapshot data
	detachedSnapshotDataset := fmt.Sprintf("%s/%s/%s", snapParams.snapshotParentDataset, sourceVolumeID, snapshotName)

	klog.Infof("Creating detached snapshot at %s from source %s", detachedSnapshotDataset, datasetName)

	// Check if detached snapshot already exists (idempotency)
	existingDatasets, err := s.apiClient.QueryAllDatasets(ctx, detachedSnapshotDataset)
	if err == nil && len(existingDatasets) > 0 {
		klog.Infof("Detached snapshot dataset %s already exists, returning existing", detachedSnapshotDataset)

		createdAt := time.Now().Unix()
		snapshotMeta := SnapshotMetadata{
			SnapshotName: detachedSnapshotDataset, // For detached, this is the dataset path
			SourceVolume: sourceVolumeID,
			DatasetName:  datasetName,
			Protocol:     snapParams.protocol,
			Detached:     true,
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
				ReadyToUse:     true,
			},
		}, nil
	}

	// Step 1: Create temporary snapshot on source for replication
	tempSnapshotName := "detached-temp-" + snapshotName
	tempSnapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   datasetName,
		Name:      tempSnapshotName,
		Recursive: false,
	}

	tempSnapshot, err := s.apiClient.CreateSnapshot(ctx, tempSnapshotParams)
	if err != nil {
		s.snapshotRegistry.Unregister(snapshotName)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot for detached snapshot: %v", err)
	}
	klog.V(4).Infof("Created temporary snapshot: %s", tempSnapshot.ID)

	// Step 2: Use CreateDetachedClone to replicate the snapshot to the target location
	// This uses zfs send/receive via TrueNAS replication API
	_, err = s.apiClient.CreateDetachedClone(ctx, tempSnapshot.ID, detachedSnapshotDataset, detachedSnapshotTimeout)
	if err != nil {
		// Cleanup temporary snapshot
		if delErr := s.apiClient.DeleteSnapshot(ctx, tempSnapshot.ID); delErr != nil {
			klog.Warningf("Failed to cleanup temporary snapshot %s: %v", tempSnapshot.ID, delErr)
		}
		s.snapshotRegistry.Unregister(snapshotName)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create detached snapshot via replication: %v", err)
	}

	// Step 3: Delete temporary snapshot from source (best effort)
	if err := s.apiClient.DeleteSnapshot(ctx, tempSnapshot.ID); err != nil {
		klog.Warningf("Failed to delete temporary snapshot %s: %v (continuing anyway)", tempSnapshot.ID, err)
	} else {
		klog.V(4).Infof("Deleted temporary snapshot: %s", tempSnapshot.ID)
	}

	klog.Infof("Successfully created detached snapshot: %s", detachedSnapshotDataset)

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: detachedSnapshotDataset, // For detached, this is the dataset path
		SourceVolume: sourceVolumeID,
		DatasetName:  datasetName,
		Protocol:     snapParams.protocol,
		Detached:     true,
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
			ReadyToUse:     true,
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

	// Route to appropriate deletion method based on snapshot type
	if snapshotMeta.Detached {
		return s.deleteDetachedSnapshot(ctx, snapshotMeta, timer)
	}
	return s.deleteNativeSnapshot(ctx, snapshotMeta, timer)
}

// deleteNativeSnapshot deletes a standard ZFS snapshot (dataset@snapshot).
func (s *ControllerService) deleteNativeSnapshot(ctx context.Context, snapshotMeta *SnapshotMetadata, timer *metrics.OperationTimer) (*csi.DeleteSnapshotResponse, error) {
	klog.Infof("Deleting native ZFS snapshot: %s", snapshotMeta.SnapshotName)

	// Delete snapshot using TrueNAS API
	if err := s.apiClient.DeleteSnapshot(ctx, snapshotMeta.SnapshotName); err != nil {
		// Check if error is because snapshot doesn't exist
		if isNotFoundError(err) {
			klog.Infof("Snapshot %s not found, assuming already deleted", snapshotMeta.SnapshotName)
			// Unregister from registry since it doesn't exist anymore
			parts := strings.Split(snapshotMeta.SnapshotName, "@")
			if len(parts) == 2 {
				s.snapshotRegistry.Unregister(parts[1])
			}
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete snapshot: %v", err)
	}

	// Unregister the snapshot name from the registry
	// Extract snapshot name from full ZFS snapshot name (dataset@snapname)
	parts := strings.Split(snapshotMeta.SnapshotName, "@")
	if len(parts) == 2 {
		s.snapshotRegistry.Unregister(parts[1])
		klog.V(4).Infof("Unregistered snapshot name %q from registry", parts[1])
	}

	klog.Infof("Successfully deleted native snapshot: %s", snapshotMeta.SnapshotName)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}

// deleteDetachedSnapshot deletes a detached snapshot (independent dataset).
func (s *ControllerService) deleteDetachedSnapshot(ctx context.Context, snapshotMeta *SnapshotMetadata, timer *metrics.OperationTimer) (*csi.DeleteSnapshotResponse, error) {
	// For detached snapshots, SnapshotName contains the dataset path
	datasetPath := snapshotMeta.SnapshotName
	klog.Infof("Deleting detached snapshot dataset: %s", datasetPath)

	// Delete the dataset using TrueNAS API
	if err := s.apiClient.DeleteDataset(ctx, datasetPath); err != nil {
		// Check if error is because dataset doesn't exist
		if isNotFoundError(err) {
			klog.Infof("Detached snapshot dataset %s not found, assuming already deleted", datasetPath)
			// Extract snapshot name from dataset path for registry cleanup
			// Path format: {snapshotParentDataset}/{sourceVolumeID}/{snapshotName}
			parts := strings.Split(datasetPath, "/")
			if len(parts) > 0 {
				snapshotName := parts[len(parts)-1]
				s.snapshotRegistry.Unregister(snapshotName)
			}
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete detached snapshot dataset: %v", err)
	}

	// Try to clean up parent directory if empty
	// Path format: {snapshotParentDataset}/{sourceVolumeID}/{snapshotName}
	// We want to delete {snapshotParentDataset}/{sourceVolumeID} if empty
	parts := strings.Split(datasetPath, "/")
	if len(parts) >= 2 {
		parentPath := strings.Join(parts[:len(parts)-1], "/")
		snapshotName := parts[len(parts)-1]

		// Try to delete the parent (volume-specific snapshot container)
		// This will fail if there are other snapshots, which is fine
		if err := s.apiClient.DeleteDataset(ctx, parentPath); err != nil {
			if !isNotFoundError(err) {
				// Log but don't fail - parent may have other snapshots
				klog.V(4).Infof("Could not delete parent snapshot container %s (may have other snapshots): %v", parentPath, err)
			}
		} else {
			klog.V(4).Infof("Deleted empty parent snapshot container: %s", parentPath)
		}

		// Unregister from registry
		s.snapshotRegistry.Unregister(snapshotName)
		klog.V(4).Infof("Unregistered snapshot name %q from registry", snapshotName)
	}

	klog.Infof("Successfully deleted detached snapshot: %s", datasetPath)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
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

	klog.V(4).Infof("ListSnapshots: filtering by snapshot ID (name: %s, detached: %v)", snapshotMeta.SnapshotName, snapshotMeta.Detached)

	// Route to appropriate query based on snapshot type
	if snapshotMeta.Detached {
		return s.listDetachedSnapshotByID(ctx, req, snapshotMeta)
	}
	return s.listNativeSnapshotByID(ctx, req, snapshotMeta)
}

// listNativeSnapshotByID queries for a native ZFS snapshot.
func (s *ControllerService) listNativeSnapshotByID(ctx context.Context, req *csi.ListSnapshotsRequest, snapshotMeta *SnapshotMetadata) (*csi.ListSnapshotsResponse, error) {
	// Query to verify snapshot exists
	filters := []interface{}{
		[]interface{}{"id", "=", snapshotMeta.SnapshotName},
	}

	snapshots, err := s.apiClient.QuerySnapshots(ctx, filters)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query snapshots: %v", err)
	}

	klog.V(4).Infof("Found %d native snapshots after filtering", len(snapshots))

	if len(snapshots) == 0 {
		// Snapshot doesn't exist, return empty list
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	// Snapshot exists - return it with the metadata we decoded
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

// listDetachedSnapshotByID queries for a detached snapshot dataset.
func (s *ControllerService) listDetachedSnapshotByID(ctx context.Context, req *csi.ListSnapshotsRequest, snapshotMeta *SnapshotMetadata) (*csi.ListSnapshotsResponse, error) {
	// For detached snapshots, SnapshotName is the dataset path
	datasets, err := s.apiClient.QueryAllDatasets(ctx, snapshotMeta.SnapshotName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query detached snapshot dataset: %v", err)
	}

	klog.V(4).Infof("Found %d detached snapshot datasets after filtering", len(datasets))

	if len(datasets) == 0 {
		// Snapshot doesn't exist, return empty list
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	// Snapshot exists - return it with the metadata we decoded
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
// This includes both native ZFS snapshots (dataset@snapshot) and detached snapshots
// (stored as independent datasets at {snapshotParentDataset}/{sourceVolumeID}/*).
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
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, share.Path)
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
				if strings.Contains(ns.Device, sourceVolumeID) {
					datasetName = strings.TrimPrefix(ns.Device, "zvol/")
					protocol = ProtocolNVMeOF
					break
				}
			}
		}
	}

	// Collect all snapshot entries (both native and detached)
	var allEntries []*csi.ListSnapshotsResponse_Entry

	// Query native ZFS snapshots for this dataset (format: dataset@snapname)
	if datasetName != "" {
		nativeEntries, nativeErr := s.listNativeSnapshotsByDataset(ctx, datasetName, sourceVolumeID, protocol)
		if nativeErr != nil {
			klog.Warningf("Failed to query native snapshots for dataset %s: %v", datasetName, nativeErr)
		} else {
			allEntries = append(allEntries, nativeEntries...)
		}
	}

	// Query detached snapshots for this source volume
	// Detached snapshots are stored at {snapshotParentDataset}/{sourceVolumeID}/*
	// We need to search for datasets that match this pattern
	detachedEntries, detachedErr := s.listDetachedSnapshotsBySourceVolume(ctx, sourceVolumeID, datasetName, protocol)
	if detachedErr != nil {
		klog.Warningf("Failed to query detached snapshots for volume %s: %v", sourceVolumeID, detachedErr)
	} else {
		allEntries = append(allEntries, detachedEntries...)
	}

	if len(allEntries) == 0 && datasetName == "" {
		// If we can't find the volume and no detached snapshots, return empty list
		klog.V(4).Infof("No snapshots found for volume %q - returning empty list", sourceVolumeID)
		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{},
		}, nil
	}

	klog.V(4).Infof("Found %d total snapshots for volume %s (native + detached)", len(allEntries), sourceVolumeID)

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = len(allEntries)
	}

	startIndex := 0
	if req.GetStartingToken() != "" {
		startIndex, err = parseSnapshotToken(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "Invalid starting token: %v", err)
		}
		if startIndex < 0 || startIndex >= len(allEntries) {
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
	}

	endIndex := startIndex + maxEntries
	if endIndex > len(allEntries) {
		endIndex = len(allEntries)
	}

	var nextToken string
	if endIndex < len(allEntries) {
		nextToken = encodeSnapshotToken(endIndex)
	}

	return &csi.ListSnapshotsResponse{
		Entries:   allEntries[startIndex:endIndex],
		NextToken: nextToken,
	}, nil
}

// listNativeSnapshotsByDataset queries native ZFS snapshots for a dataset.
func (s *ControllerService) listNativeSnapshotsByDataset(ctx context.Context, datasetName, sourceVolumeID, protocol string) ([]*csi.ListSnapshotsResponse_Entry, error) {
	filters := []interface{}{
		[]interface{}{"dataset", "=", datasetName},
	}

	snapshots, err := s.apiClient.QuerySnapshots(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots: %w", err)
	}

	klog.V(4).Infof("Found %d native snapshots for dataset %s", len(snapshots), datasetName)

	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, len(snapshots))
	for _, snapshot := range snapshots {
		// Create snapshot metadata
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			SourceVolume: sourceVolumeID,
			DatasetName:  snapshot.Dataset,
			Protocol:     protocol,
			Detached:     false,
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
				SourceVolumeId: sourceVolumeID,
				CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
				ReadyToUse:     true,
			},
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// listDetachedSnapshotsBySourceVolume queries detached snapshot datasets for a source volume.
// Detached snapshots are stored at {snapshotParentDataset}/{sourceVolumeID}/{snapshotName}.
// We search for all datasets that match the pattern */snapshots/{sourceVolumeID}/*.
func (s *ControllerService) listDetachedSnapshotsBySourceVolume(ctx context.Context, sourceVolumeID, datasetName, protocol string) ([]*csi.ListSnapshotsResponse_Entry, error) {
	// Query all datasets and filter for detached snapshot pattern
	// Pattern: {parentDataset}/snapshots/{sourceVolumeID}/{snapshotName}
	allDatasets, err := s.apiClient.QueryAllDatasets(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	// Look for datasets matching the detached snapshot pattern
	// Format: .../snapshots/{sourceVolumeID}/{snapshotName}
	searchPattern := "/snapshots/" + sourceVolumeID + "/"

	// Pre-count matching datasets to pre-allocate the slice
	matchCount := 0
	for _, dataset := range allDatasets {
		idx := strings.Index(dataset.Name, searchPattern)
		if idx == -1 {
			continue
		}
		afterPattern := dataset.Name[idx+len(searchPattern):]
		if !strings.Contains(afterPattern, "/") {
			matchCount++
		}
	}

	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, matchCount)
	for _, dataset := range allDatasets {
		// Check if this dataset matches the detached snapshot pattern
		idx := strings.Index(dataset.Name, searchPattern)
		if idx == -1 {
			continue
		}

		// Extract snapshot name from the dataset path
		// Path format: {prefix}/snapshots/{sourceVolumeID}/{snapshotName}
		afterPattern := dataset.Name[idx+len(searchPattern):]
		// If there are more path components, this is not a direct snapshot dataset
		if strings.Contains(afterPattern, "/") {
			continue
		}
		snapshotName := afterPattern

		klog.V(4).Infof("Found detached snapshot dataset: %s (snapshot name: %s)", dataset.Name, snapshotName)

		// Create snapshot metadata
		snapshotMeta := SnapshotMetadata{
			SnapshotName: dataset.Name, // For detached, this is the dataset path
			SourceVolume: sourceVolumeID,
			DatasetName:  datasetName, // Original source dataset (may be empty if source volume deleted)
			Protocol:     protocol,
			Detached:     true,
			CreatedAt:    time.Now().Unix(),
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for detached snapshot %s: %v", dataset.Name, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: sourceVolumeID,
				CreationTime:   timestamppb.New(time.Unix(snapshotMeta.CreatedAt, 0)),
				ReadyToUse:     true,
			},
		}
		entries = append(entries, entry)
	}

	klog.V(4).Infof("Found %d detached snapshots for source volume %s", len(entries), sourceVolumeID)
	return entries, nil
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
	// Note: Without additional context, we can't fully populate source volume info
	// This is acceptable per CSI spec - ListSnapshots without filters is mainly for discovery
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapshot := snapshots[i]

		// Create minimal snapshot metadata
		// We don't know the source volume or protocol without additional queries
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			DatasetName:  snapshot.Dataset,
			Protocol:     "",
			CreatedAt:    time.Now().Unix(),
			SourceVolume: "",
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v", snapshot.ID, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: "", // Unknown without additional context
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

// createVolumeFromSnapshot creates a new volume from a snapshot by cloning.
func (s *ControllerService) createVolumeFromSnapshot(ctx context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("=== createVolumeFromSnapshot CALLED === Volume: %s, SnapshotID: %s", req.GetName(), snapshotID)
	klog.V(4).Infof("Full request: %+v", req)

	// Decode snapshot metadata
	snapshotMeta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		klog.Warningf("Failed to decode snapshot ID %s: %v. Treating as not found.", snapshotID, err)
		return nil, status.Errorf(codes.NotFound, "Snapshot not found: %s", snapshotID)
	}

	// Validate and extract clone parameters
	cloneParams, err := s.validateCloneParameters(req, snapshotMeta)
	if err != nil {
		return nil, err
	}

	// Clone the snapshot
	clonedDataset, err := s.executeSnapshotClone(ctx, snapshotMeta, cloneParams)
	if err != nil {
		return nil, err
	}

	// Wait for ZFS metadata sync for NVMe-oF volumes
	s.waitForZFSSyncIfNVMeOF(snapshotMeta.Protocol)

	// Get server and subsystemNQN parameters
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}
	server, subsystemNQN, err := s.getVolumeParametersForSnapshot(ctx, params, snapshotMeta, clonedDataset)
	if err != nil {
		return nil, err
	}

	// Route to protocol-specific volume setup
	return s.setupVolumeFromClone(ctx, req, clonedDataset, snapshotMeta.Protocol, server, subsystemNQN, snapshotID)
}

// cloneParameters holds validated parameters for snapshot cloning.
type cloneParameters struct {
	pool                         string
	parentDataset                string
	newVolumeName                string
	newDatasetName               string
	detachedVolumesFromSnapshots bool // Create detached (independent) clone via zfs send/receive
}

// validateCloneParameters validates and extracts parameters needed for cloning.
func (s *ControllerService) validateCloneParameters(req *csi.CreateVolumeRequest, snapshotMeta *SnapshotMetadata) (*cloneParameters, error) {
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required")
	}

	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Check if detached volumes from snapshots is enabled
	// This creates independent clones using zfs send/receive instead of ZFS clone
	detachedVolumesFromSnapshots := params["detachedVolumesFromSnapshots"] == VolumeContextValueTrue

	newVolumeName := req.GetName()
	newDatasetName := fmt.Sprintf("%s/%s", parentDataset, newVolumeName)

	klog.Infof("Cloning snapshot %s (dataset: %s) to new volume %s (detached: %v)",
		snapshotMeta.SnapshotName, snapshotMeta.DatasetName, newVolumeName, detachedVolumesFromSnapshots)

	return &cloneParameters{
		pool:                         pool,
		parentDataset:                parentDataset,
		newVolumeName:                newVolumeName,
		newDatasetName:               newDatasetName,
		detachedVolumesFromSnapshots: detachedVolumesFromSnapshots,
	}, nil
}

// Default timeout for detached clone operations (zfs send/receive can take a while for large volumes).
const detachedCloneTimeout = 30 * time.Minute

// executeSnapshotClone performs the actual snapshot clone operation.
// For native snapshots:
//   - If detachedVolumesFromSnapshots is enabled, uses zfs send/receive to create an independent clone.
//   - Otherwise, uses the standard ZFS clone which maintains a parent-child relationship.
//
// For detached snapshots:
//   - Always creates a new snapshot on the detached snapshot dataset and clones from it,
//     since detached snapshots are already independent datasets (not ZFS snapshots).
func (s *ControllerService) executeSnapshotClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	// Handle detached snapshots differently - they're datasets, not ZFS snapshots
	if snapshotMeta.Detached {
		return s.executeCloneFromDetachedSnapshot(ctx, snapshotMeta, params)
	}

	// For native snapshots, use the configured clone method
	if params.detachedVolumesFromSnapshots {
		return s.executeDetachedClone(ctx, snapshotMeta, params)
	}
	return s.executeStandardClone(ctx, snapshotMeta, params)
}

// executeCloneFromDetachedSnapshot clones from a detached snapshot dataset.
// Since detached snapshots are independent datasets (not ZFS snapshots),
// we need to create a temporary snapshot on the dataset and then clone from it.
func (s *ControllerService) executeCloneFromDetachedSnapshot(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	// For detached snapshots, SnapshotName contains the dataset path (not a ZFS snapshot)
	sourceDataset := snapshotMeta.SnapshotName

	klog.Infof("Cloning from detached snapshot dataset %s to %s (detachedVolumesFromSnapshots: %v)",
		sourceDataset, params.newDatasetName, params.detachedVolumesFromSnapshots)

	// Create a temporary snapshot on the detached snapshot dataset for cloning
	tempSnapshotName := "clone-temp-" + params.newVolumeName
	tempSnapshotFullName := sourceDataset + "@" + tempSnapshotName

	tempSnapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   sourceDataset,
		Name:      tempSnapshotName,
		Recursive: false,
	}

	tempSnapshot, err := s.apiClient.CreateSnapshot(ctx, tempSnapshotParams)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot on detached snapshot dataset: %v", err)
	}
	klog.V(4).Infof("Created temporary snapshot: %s", tempSnapshot.ID)

	// Create a temporary SnapshotMetadata for the clone operation
	tempSnapshotMeta := &SnapshotMetadata{
		SnapshotName: tempSnapshotFullName,
		SourceVolume: snapshotMeta.SourceVolume,
		DatasetName:  sourceDataset,
		Protocol:     snapshotMeta.Protocol,
		Detached:     false, // The temp snapshot is a native ZFS snapshot
	}

	var clonedDataset *tnsapi.Dataset
	if params.detachedVolumesFromSnapshots {
		// Use zfs send/receive for fully independent clone
		clonedDataset, err = s.executeDetachedClone(ctx, tempSnapshotMeta, params)
	} else {
		// Use standard ZFS clone
		clonedDataset, err = s.executeStandardClone(ctx, tempSnapshotMeta, params)
	}

	// Cleanup temporary snapshot (best effort)
	if delErr := s.apiClient.DeleteSnapshot(ctx, tempSnapshotFullName); delErr != nil {
		klog.Warningf("Failed to delete temporary snapshot %s: %v (continuing anyway)", tempSnapshotFullName, delErr)
	} else {
		klog.V(4).Infof("Deleted temporary snapshot: %s", tempSnapshotFullName)
	}

	if err != nil {
		return nil, err
	}

	return clonedDataset, nil
}

// executeStandardClone performs a standard ZFS clone operation.
// The resulting dataset maintains a parent-child relationship with the source snapshot.
func (s *ControllerService) executeStandardClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Creating standard (dependent) clone from snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

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

	klog.Infof("Successfully created standard clone: %s", clonedDataset.Name)
	return clonedDataset, nil
}

// executeDetachedClone performs a detached clone operation using zfs send/receive.
// The resulting dataset is completely independent with no parent-child relationship.
// This is useful when you want to:
// - Delete the source snapshot without affecting the clone
// - Avoid ZFS clone dependency chains
// - Create truly independent copies of volumes.
func (s *ControllerService) executeDetachedClone(ctx context.Context, snapshotMeta *SnapshotMetadata, params *cloneParameters) (*tnsapi.Dataset, error) {
	klog.Infof("Creating detached (independent) clone from snapshot %s to dataset %s", snapshotMeta.SnapshotName, params.newDatasetName)

	// Use the TrueNAS replication API to perform zfs send/receive
	// This creates an independent copy with no parent-child relationship
	clonedDataset, err := s.apiClient.CreateDetachedClone(ctx, snapshotMeta.SnapshotName, params.newDatasetName, detachedCloneTimeout)
	if err != nil {
		klog.Errorf("Failed to create detached clone: %v", err)
		// Try to cleanup any partially created dataset
		s.cleanupPartialClone(ctx, params.newDatasetName)
		return nil, status.Errorf(codes.Internal, "Failed to create detached clone: %v", err)
	}

	klog.Infof("Successfully created detached clone: %s", clonedDataset.Name)
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
				if strings.Contains(ns.Device, sourceVolumeID) {
					// Found the namespace - get its subsystem by ID
					subsystemID := ns.Subsystem
					if subsystemID > 0 {
						// Query all subsystems and find the matching one
						allSubsystems, subsysErr := s.apiClient.ListAllNVMeOFSubsystems(ctx)
						if subsysErr != nil {
							klog.Warningf("Failed to query NVMe-oF subsystems: %v", subsysErr)
						} else {
							for _, subsys := range allSubsystems {
								if subsys.ID == subsystemID {
									subsystemNQN = subsys.NQN
									klog.V(4).Infof("Found subsystemNQN %s from source volume namespace", subsystemNQN)
									break
								}
							}
						}
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
