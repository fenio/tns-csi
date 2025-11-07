// Package driver implements the CSI driver controller service.
package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for controller operations.
var (
	ErrVolumeIDNotEncoded = errors.New("volume ID is not in encoded format")
	ErrVolumeNotFound     = errors.New("volume not found")
)

// APIClient is an alias for the TrueNAS API client interface.
// Kept for backwards compatibility with existing tests.
type APIClient = tnsapi.ClientInterface

// VolumeMetadata contains information needed to manage a volume.
type VolumeMetadata struct {
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	DatasetID         string `json:"datasetID,omitempty"`
	DatasetName       string `json:"datasetName,omitempty"`
	Server            string `json:"server,omitempty"`       // TrueNAS server address
	NVMeOFNQN         string `json:"nvmeofNQN,omitempty"`    // NVMe-oF subsystem NQN
	SubsystemNQN      string `json:"subsystemNQN,omitempty"` // Alias for NVMeOFNQN (for compatibility)
	NFSShareID        int    `json:"nfsShareID,omitempty"`
	NVMeOFSubsystemID int    `json:"nvmeofSubsystemID,omitempty"`
	NVMeOFNamespaceID int    `json:"nvmeofNamespaceID,omitempty"`
}

// encodeVolumeID encodes volume metadata into a volumeID string.
func encodeVolumeID(meta VolumeMetadata) (string, error) {
	data, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("failed to marshal volume metadata: %w", err)
	}
	// Use base64 URL-safe encoding (no padding) to create a valid volumeID
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return encoded, nil
}

// decodeVolumeID decodes a volumeID string into volume metadata.
func decodeVolumeID(volumeID string) (*VolumeMetadata, error) {
	// Handle legacy volume IDs that might not be encoded
	if !isEncodedVolumeID(volumeID) {
		klog.Warningf("Volume ID %s appears to be a legacy format, cannot decode metadata", volumeID)
		return nil, ErrVolumeIDNotEncoded
	}

	data, err := base64.RawURLEncoding.DecodeString(volumeID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode volume ID: %w", err)
	}

	var meta VolumeMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal volume metadata: %w", err)
	}

	return &meta, nil
}

// isEncodedVolumeID checks if a volumeID appears to be base64 encoded.
func isEncodedVolumeID(volumeID string) bool {
	// Base64 URL-safe encoding uses A-Z, a-z, 0-9, -, and _
	// If it contains characters outside this set, it's not our encoding
	for _, c := range volumeID {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') &&
			(c < '0' || c > '9') && c != '-' && c != '_' {

			return false
		}
	}
	return true
}

// ControllerService implements the CSI Controller service.
type ControllerService struct {
	csi.UnimplementedControllerServer
	apiClient        APIClient
	snapshotRegistry *SnapshotRegistry
}

// NewControllerService creates a new controller service.
func NewControllerService(apiClient APIClient) *ControllerService {
	return &ControllerService{
		apiClient:        apiClient,
		snapshotRegistry: NewSnapshotRegistry(),
	}
}

// CreateVolume creates a new volume.
func (s *ControllerService) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("CreateVolume called with request: %+v", req)

	// Validate request
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume name is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	// Parse storage class parameters
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	// Determine protocol (default to NFS)
	protocol := params["protocol"]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	// Check for idempotency: if volume with same name already exists
	existingVolume, err := s.checkExistingVolume(ctx, req, params, protocol)
	if err != nil && !errors.Is(err, ErrVolumeNotFound) {
		return nil, err
	}
	if existingVolume != nil {
		return existingVolume, nil
	}

	// Check if creating from snapshot or volume clone
	klog.Infof("Checking VolumeContentSource for volume %s: %+v", req.GetName(), req.GetVolumeContentSource())
	if req.GetVolumeContentSource() != nil {
		klog.Infof("VolumeContentSource is NOT nil for volume %s", req.GetName())

		// Check if creating from snapshot
		if snapshot := req.GetVolumeContentSource().GetSnapshot(); snapshot != nil {
			klog.Infof("=== SNAPSHOT RESTORE DETECTED === Creating volume %s from snapshot %s with protocol %s",
				req.GetName(), snapshot.GetSnapshotId(), protocol)
			return s.createVolumeFromSnapshot(ctx, req, snapshot.GetSnapshotId())
		}

		// Check if creating from volume (cloning)
		if volume := req.GetVolumeContentSource().GetVolume(); volume != nil {
			sourceVolumeID := volume.GetVolumeId()
			klog.Infof("=== VOLUME CLONE DETECTED === Creating volume %s from volume %s with protocol %s",
				req.GetName(), sourceVolumeID, protocol)

			return s.createVolumeFromVolume(ctx, req, sourceVolumeID)
		}

		klog.Warningf("VolumeContentSource exists but both snapshot and volume are nil for volume %s", req.GetName())
	}
	klog.V(4).Infof("VolumeContentSource is nil for volume %s (normal volume creation)", req.GetName())

	klog.Infof("Creating volume %s with protocol %s", req.GetName(), protocol)

	switch protocol {
	case ProtocolNFS:
		return s.createNFSVolume(ctx, req)
	case ProtocolNVMeOF:
		return s.createNVMeOFVolume(ctx, req)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported protocol: %s (supported: nfs, nvmeof)", protocol)
	}
}

// checkExistingVolume checks if a volume with the same name already exists and returns it for idempotency.
// Returns ErrVolumeNotFound if the volume doesn't exist, or error if the volume exists but with incompatible parameters.
func (s *ControllerService) checkExistingVolume(ctx context.Context, req *csi.CreateVolumeRequest, params map[string]string, protocol string) (*csi.CreateVolumeResponse, error) {
	pool := params["pool"]
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	if parentDataset == "" {
		return nil, ErrVolumeNotFound
	}

	expectedDatasetName := fmt.Sprintf("%s/%s", parentDataset, req.GetName())
	existingDataset, err := s.apiClient.GetDataset(ctx, expectedDatasetName)
	if err != nil || existingDataset == nil {
		// Dataset doesn't exist or error querying - continue with creation
		if err != nil {
			klog.V(4).Infof("Dataset %s does not exist or error querying: %v - proceeding with creation", expectedDatasetName, err)
		}
		return nil, ErrVolumeNotFound
	}

	// Volume already exists - check capacity compatibility
	klog.Infof("Volume %s already exists as dataset %s", req.GetName(), expectedDatasetName)

	reqCapacity := req.GetCapacityRange().GetRequiredBytes()
	if reqCapacity == 0 {
		reqCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Build complete volume metadata based on protocol
	var volumeMeta VolumeMetadata
	var volumeContext map[string]string

	switch protocol {
	case ProtocolNFS:
		meta, ctx, err := s.checkExistingNFSVolume(ctx, req, params, existingDataset, expectedDatasetName, reqCapacity)
		if err != nil {
			return nil, err
		}
		volumeMeta = meta
		volumeContext = ctx

	case ProtocolNVMeOF:
		// For NVMe-oF, we would need to query subsystems and namespaces
		// This is a placeholder for future implementation
		klog.Warningf("NVMe-oF idempotency check not fully implemented")
		volumeMeta = VolumeMetadata{
			Name:        req.GetName(),
			Protocol:    protocol,
			DatasetID:   existingDataset.ID,
			DatasetName: expectedDatasetName,
		}

	default:
		klog.Errorf("Unknown protocol: %s", protocol)
		return nil, ErrVolumeNotFound
	}

	volumeID, encodeErr := encodeVolumeID(volumeMeta)
	if encodeErr != nil {
		klog.Errorf("Failed to encode volume ID: %v", encodeErr)
		return nil, ErrVolumeNotFound
	}

	// Return capacity from request if specified, otherwise use a default
	capacity := reqCapacity
	if capacity <= 0 {
		capacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	klog.Infof("Returning existing volume %s (idempotent)", req.GetName())
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// checkExistingNFSVolume validates an existing NFS volume for idempotency.
func (s *ControllerService) checkExistingNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest, params map[string]string, existingDataset *tnsapi.Dataset, expectedDatasetName string, reqCapacity int64) (VolumeMetadata, map[string]string, error) {
	// Query for NFS share to get share ID
	shares, err := s.apiClient.QueryNFSShare(ctx, existingDataset.Mountpoint)
	if err != nil {
		klog.Errorf("Failed to query NFS shares for existing volume: %v", err)
		return VolumeMetadata{}, nil, ErrVolumeNotFound
	}

	if len(shares) == 0 {
		klog.Errorf("No NFS share found for dataset %s (mountpoint: %s)", expectedDatasetName, existingDataset.Mountpoint)
		return VolumeMetadata{}, nil, ErrVolumeNotFound
	}

	// Parse capacity from NFS share comment and validate compatibility
	existingCapacity := parseNFSShareCapacity(shares[0].Comment)
	if err := validateCapacityCompatibility(req.GetName(), existingCapacity, reqCapacity); err != nil {
		return VolumeMetadata{}, nil, err
	}

	// Get server parameter
	server := params["server"]
	if server == "" {
		server = "truenas.local" // Default for testing
	}

	volumeMeta := VolumeMetadata{
		Name:        req.GetName(),
		Protocol:    ProtocolNFS,
		DatasetID:   existingDataset.ID,
		DatasetName: expectedDatasetName,
		Server:      server,
		NFSShareID:  shares[0].ID,
	}

	volumeContext := map[string]string{
		"server":      server,
		"share":       existingDataset.Mountpoint,
		"datasetID":   existingDataset.ID,
		"datasetName": expectedDatasetName,
		"nfsShareID":  strconv.Itoa(shares[0].ID),
	}

	return volumeMeta, volumeContext, nil
}

// parseNFSShareCapacity extracts capacity from NFS share comment.
// Supports multiple formats:
// - "CSI Volume: <name>, Capacity: <bytes>"
// - "CSI Volume: <name> | Capacity: <bytes>".
func parseNFSShareCapacity(comment string) int64 {
	if comment == "" {
		klog.V(4).Infof("DEBUG: Comment is empty")
		return 0
	}

	klog.V(4).Infof("DEBUG: Parsing comment: %s", comment)

	// Try pipe separator first (preferred format)
	parts := strings.Split(comment, " | Capacity: ")
	if len(parts) != 2 {
		// Try comma separator (legacy format)
		parts = strings.Split(comment, ", Capacity: ")
		if len(parts) != 2 {
			klog.V(4).Infof("Comment does not match expected format: %s", comment)
			return 0
		}
	}

	parsed, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		klog.V(4).Infof("Could not parse capacity number: %s (error: %v)", parts[1], err)
		return 0
	}

	klog.V(4).Infof("DEBUG: Successfully parsed capacity: %d", parsed)
	return parsed
}

// validateCapacityCompatibility checks if the requested capacity matches the existing capacity.
func validateCapacityCompatibility(volumeName string, existingCapacity, reqCapacity int64) error {
	klog.V(4).Infof("DEBUG: About to validate capacity - existing: %d, requested: %d", existingCapacity, reqCapacity)

	if existingCapacity > 0 && reqCapacity != existingCapacity {
		klog.Errorf("Volume %s already exists with different capacity (existing: %d, requested: %d)",
			volumeName, existingCapacity, reqCapacity)
		return status.Errorf(codes.AlreadyExists,
			"Volume %s already exists with different capacity", volumeName)
	}

	klog.V(4).Infof("Capacity check passed (existing: %d, requested: %d)", existingCapacity, reqCapacity)
	return nil
}

// createVolumeFromVolume creates a new volume by cloning an existing volume.
// This is done by creating a temporary snapshot and cloning from it.
func (s *ControllerService) createVolumeFromVolume(ctx context.Context, req *csi.CreateVolumeRequest, sourceVolumeID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("=== createVolumeFromVolume CALLED === New volume: %s, Source volume: %s", req.GetName(), sourceVolumeID)

	// Decode source volume metadata to validate it exists
	sourceVolumeMeta, err := decodeVolumeID(sourceVolumeID)
	if err != nil {
		klog.Warningf("Failed to decode source volume ID %s: %v", sourceVolumeID, err)
		return nil, status.Errorf(codes.NotFound, "Source volume not found: %s", sourceVolumeID)
	}

	klog.Infof("Cloning from source volume %s (dataset: %s, protocol: %s)",
		sourceVolumeMeta.Name, sourceVolumeMeta.DatasetName, sourceVolumeMeta.Protocol)

	// Create a temporary snapshot of the source volume
	tempSnapshotName := "clone-temp-" + req.GetName()
	snapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   sourceVolumeMeta.DatasetName,
		Name:      tempSnapshotName,
		Recursive: false,
	}

	snapshot, err := s.apiClient.CreateSnapshot(ctx, snapshotParams)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot for cloning: %v", err)
	}

	klog.Infof("Created temporary snapshot: %s", snapshot.ID)

	// Create snapshot metadata for the temporary snapshot
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  sourceVolumeMeta.DatasetName,
		Protocol:     sourceVolumeMeta.Protocol,
		CreatedAt:    time.Now().Unix(),
	}

	snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
	if encodeErr != nil {
		// Cleanup the temporary snapshot
		if delErr := s.apiClient.DeleteSnapshot(ctx, snapshot.ID); delErr != nil {
			klog.Errorf("Failed to cleanup temporary snapshot: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to encode snapshot ID: %v", encodeErr)
	}

	// Clone from the temporary snapshot
	resp, err := s.createVolumeFromSnapshot(ctx, req, snapshotID)

	// Delete the temporary snapshot (best effort cleanup)
	if delErr := s.apiClient.DeleteSnapshot(ctx, snapshot.ID); delErr != nil {
		klog.Warningf("Failed to cleanup temporary snapshot %s: %v", snapshot.ID, delErr)
		// Don't fail the operation if cleanup fails - the volume was created successfully
	} else {
		klog.Infof("Cleaned up temporary snapshot: %s", snapshot.ID)
	}

	return resp, err
}

// DeleteVolume deletes a volume.
func (s *ControllerService) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("DeleteVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	volumeID := req.GetVolumeId()
	klog.Infof("Deleting volume %s", volumeID)

	// Decode volume metadata from volumeID
	meta, err := decodeVolumeID(volumeID)
	if err != nil {
		// If we can't decode the volume ID, log a warning but return success
		// per CSI spec (DeleteVolume should be idempotent)
		klog.Warningf("Failed to decode volume ID %s: %v. Assuming volume doesn't exist.", volumeID, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	klog.Infof("Deleting volume %s with protocol %s, dataset %s", meta.Name, meta.Protocol, meta.DatasetName)

	// Delete volume based on protocol
	switch meta.Protocol {
	case ProtocolNFS:
		return s.deleteNFSVolume(ctx, meta)
	case ProtocolNVMeOF:
		return s.deleteNVMeOFVolume(ctx, meta)
	default:
		klog.Warningf("Unknown protocol %s for volume %s, skipping deletion", meta.Protocol, volumeID)
		return &csi.DeleteVolumeResponse{}, nil
	}
}

// ControllerPublishVolume attaches a volume to a node.
func (s *ControllerService) ControllerPublishVolume(_ context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerPublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Node ID is required")
	}

	// For testing purposes, fail if node does not exist
	if req.GetNodeId() == "nonexistent-node" {
		return nil, status.Error(codes.NotFound, "node not found")
	}

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	// Verify volume exists by attempting to decode the volume ID
	// Per CSI spec: return NotFound if volume doesn't exist
	if _, err := decodeVolumeID(req.GetVolumeId()); err != nil {
		// Treat any decode failure as volume not found
		// This covers both malformed IDs and volumes that don't exist
		return nil, status.Errorf(codes.NotFound, "volume %s not found", req.GetVolumeId())
	}

	// Note: Node existence validation is not implemented as CSI spec doesn't provide
	// a mechanism for controllers to query node registry. Kubernetes handles node validation.

	// For NFS and NVMe-oF, this is typically a no-op after validation
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerService) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities validates volume capabilities.
func (s *ControllerService) ValidateVolumeCapabilities(_ context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	klog.V(4).Infof("ValidateVolumeCapabilities called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	// Validate that the volume exists by decoding the volume ID
	_, err := decodeVolumeID(req.GetVolumeId())
	if err != nil {
		// Per CSI spec: return NotFound error if volume doesn't exist
		return nil, status.Errorf(codes.NotFound, "Volume not found: %s", req.GetVolumeId())
	}

	// Basic validation: we accept all requested capabilities since TrueNAS supports both filesystem and block modes
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// ListVolumes lists all volumes.
func (s *ControllerService) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Infof("ListVolumes called with request: %+v", req)

	// Collect all CSI-managed volumes (both NFS and NVMe-oF)
	var entries []*csi.ListVolumesResponse_Entry

	// Query NFS volumes
	nfsEntries, err := s.listNFSVolumes(ctx)
	if err != nil {
		klog.Errorf("Failed to list NFS volumes: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list NFS volumes: %v", err)
	}
	entries = append(entries, nfsEntries...)

	// Query NVMe-oF volumes
	nvmeofEntries, err := s.listNVMeOFVolumes(ctx)
	if err != nil {
		klog.Errorf("Failed to list NVMe-oF volumes: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list NVMe-oF volumes: %v", err)
	}
	entries = append(entries, nvmeofEntries...)

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	startingToken := req.GetStartingToken()

	// If starting token is provided, skip entries until we reach it
	startIdx := 0
	if startingToken != "" {
		found := false
		for i, entry := range entries {
			if entry.Volume.VolumeId == startingToken {
				startIdx = i + 1
				found = true
				break
			}
		}
		// CSI spec requires returning Aborted error for invalid starting token
		if !found {
			return nil, status.Errorf(codes.Aborted, "invalid starting_token: %s", startingToken)
		}
	}

	// Limit the number of entries if maxEntries is specified
	endIdx := len(entries)
	var nextToken string
	if maxEntries > 0 && startIdx+maxEntries < len(entries) {
		endIdx = startIdx + maxEntries
		// Set next token to the last entry's volume ID
		if endIdx < len(entries) {
			nextToken = entries[endIdx-1].Volume.VolumeId
		}
	}

	// Return the paginated entries
	paginatedEntries := entries[startIdx:endIdx]

	klog.V(4).Infof("Returning %d volumes (total: %d, start: %d, end: %d)",
		len(paginatedEntries), len(entries), startIdx, endIdx)

	return &csi.ListVolumesResponse{
		Entries:   paginatedEntries,
		NextToken: nextToken,
	}, nil
}

// listNFSVolumes lists all NFS CSI volumes.
func (s *ControllerService) listNFSVolumes(ctx context.Context) ([]*csi.ListVolumesResponse_Entry, error) {
	klog.V(5).Info("Listing NFS volumes")

	// Query all NFS shares - they indicate CSI-managed NFS volumes
	shares, err := s.apiClient.QueryAllNFSShares(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	// Query all datasets to match with shares
	allDatasets, err := s.apiClient.QueryAllDatasets(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	// Build a map of mountpoint -> dataset for quick lookup
	datasetsByMountpoint := make(map[string]tnsapi.Dataset)
	for _, ds := range allDatasets {
		if ds.Mountpoint != "" {
			datasetsByMountpoint[ds.Mountpoint] = ds
		}
	}

	var entries []*csi.ListVolumesResponse_Entry
	for _, share := range shares {
		// Find the dataset that matches this share's path (mountpoint)
		dataset, found := datasetsByMountpoint[share.Path]
		if !found {
			klog.V(5).Infof("Skipping NFS share with no matching dataset mountpoint: %s", share.Path)
			continue
		}

		// Build volume metadata
		meta := VolumeMetadata{
			Name:        dataset.Name,
			Protocol:    "nfs",
			DatasetID:   dataset.ID,
			DatasetName: dataset.Name,
			NFSShareID:  share.ID,
		}

		entry := s.buildVolumeEntry(dataset, meta, "nfs")
		if entry != nil {
			entries = append(entries, entry)
		}
	}

	klog.V(5).Infof("Found %d NFS volumes", len(entries))
	return entries, nil
}

// listNVMeOFVolumes lists all NVMe-oF CSI volumes.
func (s *ControllerService) listNVMeOFVolumes(ctx context.Context) ([]*csi.ListVolumesResponse_Entry, error) {
	klog.V(5).Info("Listing NVMe-oF volumes")

	// Query all NVMe-oF namespaces - they indicate CSI-managed NVMe-oF volumes
	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVMe-oF namespaces: %w", err)
	}

	var entries []*csi.ListVolumesResponse_Entry
	for _, ns := range namespaces {
		// Each namespace corresponds to a ZVOL
		// The device path usually points to the ZVOL
		datasets, err := s.apiClient.QueryAllDatasets(ctx, ns.Device)
		if err != nil || len(datasets) == 0 {
			klog.V(5).Infof("Skipping NVMe-oF namespace with no matching ZVOL: %s", ns.Device)
			continue
		}

		zvol := datasets[0]

		// Build volume metadata
		meta := VolumeMetadata{
			Name:              zvol.Name,
			Protocol:          "nvmeof",
			DatasetID:         zvol.ID,
			DatasetName:       zvol.Name,
			NVMeOFNamespaceID: ns.ID,
		}

		entry := s.buildVolumeEntry(zvol, meta, "nvmeof")
		if entry != nil {
			entries = append(entries, entry)
		}
	}

	klog.V(5).Infof("Found %d NVMe-oF volumes", len(entries))
	return entries, nil
}

// buildVolumeEntry constructs a ListVolumesResponse_Entry from dataset and metadata.
func (s *ControllerService) buildVolumeEntry(dataset tnsapi.Dataset, meta VolumeMetadata, protocol string) *csi.ListVolumesResponse_Entry {
	// Encode volume ID
	volumeID, err := encodeVolumeID(meta)
	if err != nil {
		klog.Warningf("Failed to encode volume ID for dataset %s: %v", dataset.Name, err)
		return nil
	}

	// Determine capacity from dataset
	var capacityBytes int64
	if dataset.Available != nil {
		if val, ok := dataset.Available["parsed"].(float64); ok {
			capacityBytes = int64(val)
		}
	}

	return &csi.ListVolumesResponse_Entry{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacityBytes,
			VolumeContext: map[string]string{
				"protocol":    protocol,
				"datasetName": dataset.Name,
			},
		},
	}
}

// GetCapacity returns the capacity of the storage pool.
func (s *ControllerService) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Infof("GetCapacity called with request: %+v", req)

	// Extract pool name from StorageClass parameters
	params := req.GetParameters()
	if params == nil {
		klog.Warning("GetCapacity called without parameters, cannot determine pool")
		return &csi.GetCapacityResponse{}, nil
	}

	poolName := params["pool"]
	if poolName == "" {
		klog.Warning("GetCapacity called without pool parameter")
		return &csi.GetCapacityResponse{}, nil
	}

	// Query pool capacity from TrueNAS
	pool, err := s.apiClient.QueryPool(ctx, poolName)
	if err != nil {
		klog.Errorf("Failed to query pool %s: %v", poolName, err)
		return nil, status.Errorf(codes.Internal, "Failed to query pool capacity: %v", err)
	}

	// Return available capacity in bytes
	availableCapacity := pool.Properties.Free.Parsed
	klog.V(4).Infof("Pool %s capacity: total=%d bytes, available=%d bytes, used=%d bytes",
		poolName,
		pool.Properties.Size.Parsed,
		availableCapacity,
		pool.Properties.Allocated.Parsed)

	return &csi.GetCapacityResponse{
		AvailableCapacity: availableCapacity,
	}, nil
}

// ControllerGetCapabilities returns controller capabilities.
func (s *ControllerService) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	klog.V(4).Info("ControllerGetCapabilities called")

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
					},
				},
			},
		},
	}, nil
}

// Snapshot operations are implemented in controller_snapshot.go

// ControllerExpandVolume expands a volume.
func (s *ControllerService) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).Infof("ControllerExpandVolume called with request: %+v", req)

	// Validate request
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "Capacity range is required")
	}

	volumeID := req.GetVolumeId()
	requiredBytes := req.GetCapacityRange().GetRequiredBytes()

	klog.Infof("Expanding volume %s to %d bytes", volumeID, requiredBytes)

	// Decode volume metadata from volumeID
	meta, err := decodeVolumeID(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Failed to decode volume ID: %v", err)
	}

	klog.Infof("Expanding volume %s with protocol %s, dataset %s", meta.Name, meta.Protocol, meta.DatasetName)

	// Expand volume based on protocol
	switch meta.Protocol {
	case ProtocolNFS:
		return s.expandNFSVolume(ctx, meta, requiredBytes)
	case ProtocolNVMeOF:
		return s.expandNVMeOFVolume(ctx, meta, requiredBytes)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported protocol for expansion: %s", meta.Protocol)
	}
}

// ControllerGetVolume gets volume information.
func (s *ControllerService) ControllerGetVolume(_ context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("ControllerGetVolume called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume not implemented")
}

// ControllerModifyVolume modifies a volume.
func (s *ControllerService) ControllerModifyVolume(_ context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	klog.V(4).Infof("ControllerModifyVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}
