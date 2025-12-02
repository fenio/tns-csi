// Package driver implements the CSI driver controller service.
package driver

import (
	"context"
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

// Error message constants.
const (
	errMsgVolumeIDRequired = "Volume ID is required"
)

// VolumeContext key constants - these are used consistently across the driver.
const (
	VolumeContextKeyProtocol          = "protocol"
	VolumeContextKeyServer            = "server"
	VolumeContextKeyShare             = "share"
	VolumeContextKeyDatasetID         = "datasetID"
	VolumeContextKeyDatasetName       = "datasetName"
	VolumeContextKeyNFSShareID        = "nfsShareID"
	VolumeContextKeyNQN               = "nqn"
	VolumeContextKeyNVMeOFSubsystemID = "nvmeofSubsystemID"
	VolumeContextKeyNVMeOFNamespaceID = "nvmeofNamespaceID"
	VolumeContextKeyNSID              = "nsid"
	VolumeContextKeyExpectedCapacity  = "expectedCapacity"
	VolumeContextKeyClonedFromSnap    = "clonedFromSnapshot"
)

// Static errors for controller operations.
var (
	ErrVolumeNotFound = errors.New("volume not found")
)

// APIClient is an alias for the TrueNAS API client interface.
// Kept for backwards compatibility with existing tests.
type APIClient = tnsapi.ClientInterface

// VolumeMetadata contains information needed to manage a volume.
// This is used internally and for building VolumeContext.
// Note: Volume ID is now just the volume name (CSI spec compliant, max 128 bytes).
// All metadata is passed via VolumeContext.
type VolumeMetadata struct {
	Name              string
	Protocol          string
	DatasetID         string
	DatasetName       string
	Server            string // TrueNAS server address
	NVMeOFNQN         string // NVMe-oF subsystem NQN
	NFSShareID        int
	NVMeOFSubsystemID int
	NVMeOFNamespaceID int
}

// buildVolumeContext creates a VolumeContext map from VolumeMetadata.
// This is the standard way to pass volume metadata through CSI.
func buildVolumeContext(meta VolumeMetadata) map[string]string {
	ctx := map[string]string{
		VolumeContextKeyProtocol: meta.Protocol,
	}

	if meta.Server != "" {
		ctx[VolumeContextKeyServer] = meta.Server
	}
	if meta.DatasetID != "" {
		ctx[VolumeContextKeyDatasetID] = meta.DatasetID
	}
	if meta.DatasetName != "" {
		ctx[VolumeContextKeyDatasetName] = meta.DatasetName
	}

	// Protocol-specific fields
	switch meta.Protocol {
	case ProtocolNFS:
		if meta.NFSShareID != 0 {
			ctx[VolumeContextKeyNFSShareID] = strconv.Itoa(meta.NFSShareID)
		}
	case ProtocolNVMeOF:
		if meta.NVMeOFNQN != "" {
			ctx[VolumeContextKeyNQN] = meta.NVMeOFNQN
		}
		if meta.NVMeOFSubsystemID != 0 {
			ctx[VolumeContextKeyNVMeOFSubsystemID] = strconv.Itoa(meta.NVMeOFSubsystemID)
		}
		if meta.NVMeOFNamespaceID != 0 {
			ctx[VolumeContextKeyNVMeOFNamespaceID] = strconv.Itoa(meta.NVMeOFNamespaceID)
		}
	}

	return ctx
}

// getProtocolFromVolumeContext determines the protocol from volume context.
// Falls back to NFS if protocol is not specified (for backwards compatibility).
func getProtocolFromVolumeContext(ctx map[string]string) string {
	if protocol := ctx[VolumeContextKeyProtocol]; protocol != "" {
		return protocol
	}
	// Infer protocol from context keys
	if ctx[VolumeContextKeyNQN] != "" {
		return ProtocolNVMeOF
	}
	if ctx[VolumeContextKeyShare] != "" || ctx[VolumeContextKeyNFSShareID] != "" {
		return ProtocolNFS
	}
	// Default to NFS for backwards compatibility
	return ProtocolNFS
}

// ControllerService implements the CSI Controller service.
type ControllerService struct {
	csi.UnimplementedControllerServer
	apiClient        APIClient
	snapshotRegistry *SnapshotRegistry
	nodeRegistry     *NodeRegistry
}

// NewControllerService creates a new controller service.
func NewControllerService(apiClient APIClient, nodeRegistry *NodeRegistry) *ControllerService {
	return &ControllerService{
		apiClient:        apiClient,
		snapshotRegistry: NewSnapshotRegistry(),
		nodeRegistry:     nodeRegistry,
	}
}

// CreateVolume creates a new volume.
func (s *ControllerService) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("CreateVolume called with request: %+v", req)

	// Log detailed debug info for troubleshooting
	s.logCreateVolumeDebugInfo(req)

	// Validate request
	if err := validateCreateVolumeRequest(req); err != nil {
		return nil, err
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
		klog.V(4).Infof("Returning existing volume for idempotency: %s", req.GetName())
		return existingVolume, nil
	}

	// Check if creating from snapshot or volume clone
	if resp, handled, err := s.handleVolumeContentSource(ctx, req, protocol); handled {
		return resp, err
	}

	klog.V(4).Infof("Creating volume %s with protocol %s", req.GetName(), protocol)

	return s.createVolumeByProtocol(ctx, req, protocol)
}

// logCreateVolumeDebugInfo logs detailed debug information for CreateVolume troubleshooting.
func (s *ControllerService) logCreateVolumeDebugInfo(req *csi.CreateVolumeRequest) {
	klog.V(4).Infof("=== CreateVolume Debug Info ===")
	klog.V(4).Infof("Volume Name: %s", req.GetName())
	klog.V(4).Infof("VolumeContentSource: %+v", req.GetVolumeContentSource())
	if req.GetVolumeContentSource() != nil {
		klog.V(4).Infof("VolumeContentSource Type: %T", req.GetVolumeContentSource().GetType())
		klog.V(4).Infof("VolumeContentSource.Snapshot: %+v", req.GetVolumeContentSource().GetSnapshot())
		klog.V(4).Infof("VolumeContentSource.Volume: %+v", req.GetVolumeContentSource().GetVolume())
	}
	klog.V(4).Infof("Parameters: %+v", req.GetParameters())
	klog.V(4).Infof("CapacityRange: %+v", req.GetCapacityRange())
	klog.V(4).Infof("VolumeCapabilities: %+v", req.GetVolumeCapabilities())
	klog.V(4).Infof("AccessibilityRequirements: %+v", req.GetAccessibilityRequirements())
	klog.V(4).Infof("Secrets: [REDACTED - %d keys]", len(req.GetSecrets()))
	klog.V(4).Infof("===============================")
}

// validateCreateVolumeRequest validates the CreateVolume request parameters.
func validateCreateVolumeRequest(req *csi.CreateVolumeRequest) error {
	if req.GetName() == "" {
		return status.Error(codes.InvalidArgument, "Volume name is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	return nil
}

// handleVolumeContentSource handles creating volumes from snapshots or clones.
// Returns (response, true, nil) if handled successfully, (nil, true, error) if handled with error,
// or (nil, false, nil) if not a content source request.
func (s *ControllerService) handleVolumeContentSource(ctx context.Context, req *csi.CreateVolumeRequest, protocol string) (*csi.CreateVolumeResponse, bool, error) {
	contentSource := req.GetVolumeContentSource()
	klog.V(4).Infof("Checking VolumeContentSource for volume %s: %+v", req.GetName(), contentSource)

	if contentSource == nil {
		klog.V(4).Infof("VolumeContentSource is nil for volume %s (normal volume creation)", req.GetName())
		return nil, false, nil
	}

	klog.V(4).Infof("VolumeContentSource is NOT nil for volume %s", req.GetName())

	// Check if creating from snapshot
	if snapshot := contentSource.GetSnapshot(); snapshot != nil {
		klog.V(4).Infof("=== SNAPSHOT RESTORE DETECTED === Creating volume %s from snapshot %s with protocol %s",
			req.GetName(), snapshot.GetSnapshotId(), protocol)
		resp, err := s.createVolumeFromSnapshot(ctx, req, snapshot.GetSnapshotId())
		if err != nil {
			klog.Errorf("Failed to create volume from snapshot: %v", err)
			return nil, true, err
		}
		return resp, true, nil
	}

	// Check if creating from volume (cloning)
	if volume := contentSource.GetVolume(); volume != nil {
		sourceVolumeID := volume.GetVolumeId()
		klog.V(4).Infof("=== VOLUME CLONE DETECTED === Creating volume %s from volume %s with protocol %s",
			req.GetName(), sourceVolumeID, protocol)
		resp, err := s.createVolumeFromVolume(ctx, req, sourceVolumeID)
		if err != nil {
			klog.Errorf("Failed to create volume from volume: %v", err)
			return nil, true, err
		}
		return resp, true, nil
	}

	klog.Warningf("VolumeContentSource exists but both snapshot and volume are nil for volume %s", req.GetName())
	return nil, false, nil
}

// createVolumeByProtocol creates a volume using the specified protocol.
func (s *ControllerService) createVolumeByProtocol(ctx context.Context, req *csi.CreateVolumeRequest, protocol string) (*csi.CreateVolumeResponse, error) {
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
	existingDataset, err := s.apiClient.Dataset(ctx, expectedDatasetName)
	if err != nil || existingDataset == nil {
		// Dataset doesn't exist or error querying - continue with creation
		if err != nil {
			klog.V(4).Infof("Dataset %s does not exist or error querying: %v - proceeding with creation", expectedDatasetName, err)
		}
		return nil, ErrVolumeNotFound
	}

	// Volume already exists - check capacity compatibility
	klog.V(4).Infof("Volume %s already exists as dataset %s", req.GetName(), expectedDatasetName)

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

	// Volume ID is now just the volume name (CSI spec compliant)
	volumeID := req.GetName()

	// Return capacity from request if specified, otherwise use a default
	capacity := reqCapacity
	if capacity <= 0 {
		capacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Ensure volume context includes protocol
	if volumeContext == nil {
		volumeContext = buildVolumeContext(volumeMeta)
	} else {
		volumeContext[VolumeContextKeyProtocol] = protocol
	}

	klog.V(4).Infof("Returning existing volume %s (idempotent)", req.GetName())
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
	klog.V(4).Infof("=== createVolumeFromVolume CALLED === New volume: %s, Source volume: %s", req.GetName(), sourceVolumeID)

	// With plain volume IDs, we need to look up the source volume's metadata from TrueNAS
	// The sourceVolumeID is now just the volume name, we need to find its dataset
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

	// Build expected dataset name for source volume
	sourceDatasetName := fmt.Sprintf("%s/%s", parentDataset, sourceVolumeID)

	// Verify source volume exists
	sourceDataset, err := s.apiClient.Dataset(ctx, sourceDatasetName)
	if err != nil || sourceDataset == nil {
		klog.Warningf("Source volume %s not found (dataset: %s): %v", sourceVolumeID, sourceDatasetName, err)
		return nil, status.Errorf(codes.NotFound, "Source volume not found: %s", sourceVolumeID)
	}

	klog.V(4).Infof("Cloning from source volume %s (dataset: %s, protocol: %s)",
		sourceVolumeID, sourceDatasetName, protocol)

	// Create a temporary snapshot of the source volume
	tempSnapshotName := "clone-temp-" + req.GetName()
	snapshotParams := tnsapi.SnapshotCreateParams{
		Dataset:   sourceDatasetName,
		Name:      tempSnapshotName,
		Recursive: false,
	}

	snapshot, err := s.apiClient.CreateSnapshot(ctx, snapshotParams)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create temporary snapshot for cloning: %v", err)
	}

	klog.V(4).Infof("Created temporary snapshot: %s", snapshot.ID)

	// Create snapshot metadata for the temporary snapshot
	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshot.ID,
		SourceVolume: sourceVolumeID,
		DatasetName:  sourceDatasetName,
		Protocol:     protocol,
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
		klog.V(4).Infof("Cleaned up temporary snapshot: %s", snapshot.ID)
	}

	return resp, err
}

// DeleteVolume deletes a volume.
func (s *ControllerService) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("DeleteVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("Deleting volume %s", volumeID)

	// With plain volume IDs, we need to look up the volume in TrueNAS to find its metadata.
	// We try to find it as both NFS and NVMe-oF volume since we don't have the protocol info.
	// First, try to find NFS shares matching this volume name
	shares, err := s.apiClient.QueryAllNFSShares(ctx, volumeID)
	if err == nil && len(shares) > 0 {
		// Found as NFS volume - find the matching dataset
		for _, share := range shares {
			if strings.HasSuffix(share.Path, "/"+volumeID) {
				// Query the dataset for this share
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, share.Path)
				if dsErr == nil && len(datasets) > 0 {
					meta := VolumeMetadata{
						Name:        volumeID,
						Protocol:    ProtocolNFS,
						DatasetID:   datasets[0].ID,
						DatasetName: datasets[0].Name,
						NFSShareID:  share.ID,
					}
					klog.V(4).Infof("Deleting NFS volume %s with dataset %s", volumeID, meta.DatasetName)
					return s.deleteNFSVolume(ctx, &meta)
				}
			}
		}
	}

	// Try to find NVMe-oF namespaces matching this volume name
	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err == nil {
		for _, ns := range namespaces {
			// Check if the namespace device path contains the volume name
			if strings.Contains(ns.Device, volumeID) {
				// Find the subsystem for this namespace
				subsystems, subErr := s.apiClient.ListAllNVMeOFSubsystems(ctx)
				if subErr == nil {
					for _, sub := range subsystems {
						if sub.ID == ns.Subsystem {
							meta := VolumeMetadata{
								Name:              volumeID,
								Protocol:          ProtocolNVMeOF,
								DatasetName:       ns.Device, // Device path is the zvol path
								NVMeOFNQN:         sub.NQN,
								NVMeOFSubsystemID: sub.ID,
								NVMeOFNamespaceID: ns.ID,
							}
							klog.V(4).Infof("Deleting NVMe-oF volume %s with dataset %s", volumeID, meta.DatasetName)
							return s.deleteNVMeOFVolume(ctx, &meta)
						}
					}
				}
			}
		}
	}

	// Volume not found in either protocol - return success per CSI spec (idempotent delete)
	klog.V(4).Infof("Volume %s not found, returning success (idempotent)", volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume attaches a volume to a node.
func (s *ControllerService) ControllerPublishVolume(_ context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerPublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Node ID is required")
	}

	nodeID := req.GetNodeId()

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	// Validate node exists in registry
	// Per CSI spec: return NotFound if node doesn't exist
	if s.nodeRegistry != nil && !s.nodeRegistry.IsRegistered(nodeID) {
		return nil, status.Errorf(codes.NotFound, "node %s not found", nodeID)
	}

	// With plain volume IDs (just the volume name), we cannot verify volume existence
	// from the ID alone. The volume context should contain the necessary metadata.
	// Trust that the CO (Kubernetes) has validated the volume exists.
	klog.V(4).Infof("ControllerPublishVolume: volume %s on node %s", req.GetVolumeId(), nodeID)

	// For NFS and NVMe-oF, this is typically a no-op after validation
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerService) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities validates volume capabilities.
func (s *ControllerService) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	klog.V(4).Infof("ValidateVolumeCapabilities called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("ValidateVolumeCapabilities: validating volume %s", volumeID)

	// Check if volume exists by searching for it in TrueNAS
	volumeExists := false

	// Check NFS volumes
	shares, err := s.apiClient.QueryAllNFSShares(ctx, volumeID)
	if err == nil {
		for _, share := range shares {
			if strings.HasSuffix(share.Path, "/"+volumeID) {
				volumeExists = true
				break
			}
		}
	}

	// Check NVMe-oF volumes if not found as NFS
	if !volumeExists {
		namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
		if err == nil {
			for _, ns := range namespaces {
				if strings.Contains(ns.Device, volumeID) {
					volumeExists = true
					break
				}
			}
		}
	}

	if !volumeExists {
		return nil, status.Errorf(codes.NotFound, "Volume %s not found", volumeID)
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
			Protocol:    ProtocolNFS,
			DatasetID:   dataset.ID,
			DatasetName: dataset.Name,
			NFSShareID:  share.ID,
		}

		entry := s.buildVolumeEntry(dataset, meta)
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
			Protocol:          ProtocolNVMeOF,
			DatasetID:         zvol.ID,
			DatasetName:       zvol.Name,
			NVMeOFNamespaceID: ns.ID,
		}

		entry := s.buildVolumeEntry(zvol, meta)
		if entry != nil {
			entries = append(entries, entry)
		}
	}

	klog.V(5).Infof("Found %d NVMe-oF volumes", len(entries))
	return entries, nil
}

// buildVolumeEntry constructs a ListVolumesResponse_Entry from dataset and metadata.
func (s *ControllerService) buildVolumeEntry(dataset tnsapi.Dataset, meta VolumeMetadata) *csi.ListVolumesResponse_Entry {
	// Volume ID is just the volume name (CSI spec compliant)
	// Extract volume name from dataset name (last path component)
	parts := strings.Split(dataset.Name, "/")
	volumeID := parts[len(parts)-1]

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
			VolumeContext: buildVolumeContext(meta),
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
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "Capacity range is required")
	}

	volumeID := req.GetVolumeId()
	requiredBytes := req.GetCapacityRange().GetRequiredBytes()

	klog.V(4).Infof("Expanding volume %s to %d bytes", volumeID, requiredBytes)

	// With plain volume IDs, we need to look up the volume in TrueNAS to find its metadata.
	// First, try to find NFS shares matching this volume name
	shares, err := s.apiClient.QueryAllNFSShares(ctx, volumeID)
	if err == nil && len(shares) > 0 {
		for _, share := range shares {
			if strings.HasSuffix(share.Path, "/"+volumeID) {
				datasets, dsErr := s.apiClient.QueryAllDatasets(ctx, share.Path)
				if dsErr == nil && len(datasets) > 0 {
					meta := &VolumeMetadata{
						Name:        volumeID,
						Protocol:    ProtocolNFS,
						DatasetID:   datasets[0].ID,
						DatasetName: datasets[0].Name,
						NFSShareID:  share.ID,
					}
					klog.V(4).Infof("Expanding NFS volume %s with dataset %s", volumeID, meta.DatasetName)
					return s.expandNFSVolume(ctx, meta, requiredBytes)
				}
			}
		}
	}

	// Try to find NVMe-oF namespaces matching this volume name
	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err == nil {
		for _, ns := range namespaces {
			if strings.Contains(ns.Device, volumeID) {
				subsystems, subErr := s.apiClient.ListAllNVMeOFSubsystems(ctx)
				if subErr == nil {
					for _, sub := range subsystems {
						if sub.ID == ns.Subsystem {
							// Extract the dataset ID from the device path
							// Device path is like /dev/zvol/tank/csi/volume-name
							// Dataset ID is tank/csi/volume-name
							datasetID := strings.TrimPrefix(ns.Device, "/dev/zvol/")
							meta := &VolumeMetadata{
								Name:              volumeID,
								Protocol:          ProtocolNVMeOF,
								DatasetID:         datasetID,
								DatasetName:       datasetID,
								NVMeOFNQN:         sub.NQN,
								NVMeOFSubsystemID: sub.ID,
								NVMeOFNamespaceID: ns.ID,
							}
							klog.V(4).Infof("Expanding NVMe-oF volume %s with dataset %s", volumeID, meta.DatasetName)
							return s.expandNVMeOFVolume(ctx, meta, requiredBytes)
						}
					}
				}
			}
		}
	}

	return nil, status.Errorf(codes.NotFound, "Volume %s not found for expansion", volumeID)
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
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}
