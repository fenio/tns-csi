package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// SnapshotMetadata contains information needed to manage a snapshot.
type SnapshotMetadata struct {
	SnapshotName string `json:"snapshotName"` // ZFS snapshot name (dataset@snapshot)
	SourceVolume string `json:"sourceVolume"` // Source volume ID
	DatasetName  string `json:"datasetName"`  // Parent dataset name
	Protocol     string `json:"protocol"`     // Protocol (nfs, nvmeof)
	CreatedAt    int64  `json:"createdAt"`    // Creation timestamp (Unix epoch)
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

	// Decode source volume metadata
	volumeMeta, err := decodeVolumeID(sourceVolumeID)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Failed to decode source volume ID: %v", err)
	}

	klog.Infof("Creating snapshot %s for volume %s (dataset: %s, protocol: %s)",
		snapshotName, volumeMeta.Name, volumeMeta.DatasetName, volumeMeta.Protocol)

	// Check if snapshot already exists (idempotency)
	existingSnapshots, err := s.apiClient.QuerySnapshots(ctx, []interface{}{
		[]interface{}{"id", "=", fmt.Sprintf("%s@%s", volumeMeta.DatasetName, snapshotName)},
	})
	if err != nil {
		klog.Warningf("Failed to query existing snapshots: %v", err)
	} else if len(existingSnapshots) > 0 {
		// Snapshot already exists, return it
		klog.Infof("Snapshot %s already exists, returning existing snapshot", snapshotName)
		snapshot := existingSnapshots[0]

		// Create snapshot metadata
		createdAt := time.Now().Unix() // Use current time as we don't have creation time from API
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			SourceVolume: sourceVolumeID,
			DatasetName:  volumeMeta.DatasetName,
			Protocol:     volumeMeta.Protocol,
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
		Dataset:   volumeMeta.DatasetName,
		Name:      snapshotName,
		Recursive: false,
	}

	snapshot, err := s.apiClient.CreateSnapshot(ctx, snapshotParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create snapshot: %v", err)
	}

	klog.Infof("Successfully created snapshot: %s", snapshot.ID)

	// Create snapshot metadata
	createdAt := time.Now().Unix()
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  volumeMeta.DatasetName,
		Protocol:     volumeMeta.Protocol,
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

	klog.Infof("Deleting ZFS snapshot: %s", snapshotMeta.SnapshotName)

	// Delete snapshot using TrueNAS API
	if err := s.apiClient.DeleteSnapshot(ctx, snapshotMeta.SnapshotName); err != nil {
		// Check if error is because snapshot doesn't exist
		if isNotFoundError(err) {
			klog.Infof("Snapshot %s not found, assuming already deleted", snapshotMeta.SnapshotName)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete snapshot: %v", err)
	}

	klog.Infof("Successfully deleted snapshot: %s", snapshotMeta.SnapshotName)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}

// ListSnapshots lists snapshots.
func (s *ControllerService) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots called with request: %+v", req)

	// Build query filters
	var filters []interface{}

	// Filter by snapshot ID if specified
	if req.GetSnapshotId() != "" {
		snapshotMeta, err := decodeSnapshotID(req.GetSnapshotId())
		if err != nil {
			// CSI spec: return empty list for non-existent or invalid snapshot IDs
			klog.V(4).Infof("Invalid snapshot ID %q, returning empty list: %v", req.GetSnapshotId(), err)
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
		filters = []interface{}{
			[]interface{}{"id", "=", snapshotMeta.SnapshotName},
		}
	} else if req.GetSourceVolumeId() != "" {
		// Filter by source volume - need to decode volume to get dataset name
		volumeMeta, err := decodeVolumeID(req.GetSourceVolumeId())
		if err != nil {
			// CSI spec: return empty list for non-existent or invalid volume IDs
			klog.V(4).Infof("Invalid source volume ID %q, returning empty list: %v", req.GetSourceVolumeId(), err)
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
		// Query snapshots for this dataset (snapshots will have format dataset@snapname)
		filters = []interface{}{
			[]interface{}{"dataset", "=", volumeMeta.DatasetName},
		}
	}

	// Query snapshots from TrueNAS
	snapshots, err := s.apiClient.QuerySnapshots(ctx, filters)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query snapshots: %v", err)
	}

	klog.V(4).Infof("Found %d snapshots", len(snapshots))

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = len(snapshots) // Return all if not specified
	}

	// Parse starting token (offset index)
	startIndex := 0
	if req.GetStartingToken() != "" {
		var err error
		startIndex, err = parseSnapshotToken(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "Invalid starting token: %v", err)
		}
		if startIndex < 0 || startIndex >= len(snapshots) {
			// Starting token is out of range, return empty list
			return &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{},
			}, nil
		}
	}

	// Calculate end index
	endIndex := startIndex + maxEntries
	if endIndex > len(snapshots) {
		endIndex = len(snapshots)
	}

	// Convert to CSI format
	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapshot := snapshots[i]
		// For each snapshot, we need to determine the source volume
		// Snapshot ID format is "dataset@snapname", dataset is the volume's dataset

		// Try to find matching volume by dataset name
		// For now, we'll create a basic snapshot metadata
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshot.ID,
			DatasetName:  snapshot.Dataset,
			Protocol:     "", // Unknown without volume context
			CreatedAt:    time.Now().Unix(),
			SourceVolume: "", // Unknown - would require querying volumes
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v", snapshot.ID, encodeErr)
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

	// Generate next token if there are more results
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
//
//nolint:gocognit // Complexity from protocol-specific handling and error cleanup - splitting would hurt readability
func (s *ControllerService) createVolumeFromSnapshot(ctx context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("=== createVolumeFromSnapshot CALLED === Volume: %s, SnapshotID: %s", req.GetName(), snapshotID)
	klog.V(4).Infof("Full request: %+v", req)

	// Decode snapshot metadata
	snapshotMeta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		// Per CSI spec: if snapshot ID is invalid/malformed, treat it as not found
		klog.Warningf("Failed to decode snapshot ID %s: %v. Treating as not found.", snapshotID, err)
		return nil, status.Errorf(codes.NotFound, "Snapshot not found: %s", snapshotID)
	}

	klog.Infof("Cloning snapshot %s (dataset: %s) to new volume %s",
		snapshotMeta.SnapshotName, snapshotMeta.DatasetName, req.GetName())

	// Get parameters from storage class
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Construct new dataset name for the cloned volume
	newVolumeName := req.GetName()
	newDatasetName := fmt.Sprintf("%s/%s", parentDataset, newVolumeName)

	klog.Infof("Cloning snapshot %s to dataset %s", snapshotMeta.SnapshotName, newDatasetName)

	// Clone the snapshot to a new dataset
	cloneParams := tnsapi.CloneSnapshotParams{
		Snapshot: snapshotMeta.SnapshotName,
		Dataset:  newDatasetName,
	}

	clonedDataset, err := s.apiClient.CloneSnapshot(ctx, cloneParams)
	if err != nil {
		// Check if dataset was partially created despite the error
		// This can happen if the clone operation succeeds but querying the dataset fails
		klog.Errorf("Failed to clone snapshot: %v. Checking if dataset was created...", err)

		// Try to cleanup any partially created dataset
		if delErr := s.apiClient.DeleteDataset(ctx, newDatasetName); delErr != nil {
			// If deletion fails with "not found", that's okay - dataset wasn't created
			if !isNotFoundError(delErr) {
				klog.Errorf("Failed to cleanup potentially partially-created dataset %s: %v", newDatasetName, delErr)
			}
		} else {
			klog.Infof("Cleaned up partially-created dataset: %s", newDatasetName)
		}

		return nil, status.Errorf(codes.Internal, "Failed to clone snapshot: %v", err)
	}

	klog.Infof("Successfully cloned snapshot to dataset: %s", clonedDataset.Name)

	// Get server and subsystemNQN parameters from StorageClass or source volume
	server, subsystemNQN, err := s.getVolumeParametersForSnapshot(ctx, params, snapshotMeta, clonedDataset)
	if err != nil {
		return nil, err
	}

	// Route to protocol-specific volume setup based on snapshot protocol
	switch snapshotMeta.Protocol {
	case ProtocolNFS:
		return s.setupNFSVolumeFromClone(ctx, req, clonedDataset, server, snapshotID)
	case ProtocolNVMeOF:
		// Validate subsystemNQN is available for NVMe-oF
		if subsystemNQN == "" {
			// Cleanup the cloned dataset
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
	default:
		// Cleanup the cloned dataset if we can't determine protocol
		klog.Errorf("Unknown protocol %s in snapshot metadata, cleaning up", snapshotMeta.Protocol)
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol in snapshot: %s", snapshotMeta.Protocol)
	}
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

	// Decode source volume metadata
	sourceVolumeMeta, decodeErr := decodeVolumeID(snapshotMeta.SourceVolume)
	if decodeErr != nil {
		// Cleanup the cloned dataset
		klog.Errorf("Failed to decode source volume ID from snapshot, cleaning up: %v", decodeErr)
		if delErr := s.apiClient.DeleteDataset(ctx, clonedDataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return "", "", status.Errorf(codes.Internal, "Failed to decode source volume metadata: %v", decodeErr)
	}

	// Use server from source volume if not provided
	if server == "" {
		server = sourceVolumeMeta.Server
		klog.V(4).Infof("Using server from source volume: %s", server)
	}

	// Use subsystem NQN from source volume if not provided (for NVMe-oF)
	if subsystemNQN == "" && snapshotMeta.Protocol == ProtocolNVMeOF {
		// Try SubsystemNQN first, fallback to NVMeOFNQN
		if sourceVolumeMeta.SubsystemNQN != "" {
			subsystemNQN = sourceVolumeMeta.SubsystemNQN
		} else {
			subsystemNQN = sourceVolumeMeta.NVMeOFNQN
		}
		klog.V(4).Infof("Using subsystemNQN from source volume: %s", subsystemNQN)
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
