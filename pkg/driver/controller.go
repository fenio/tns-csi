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
	msgVolumeIsHealthy     = "Volume is healthy"
)

// Default values.
const (
	defaultServerAddress = "defaultServerAddress"
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
	VolumeContextValueTrue            = "true"
	VolumeContextValueFalse           = "false"
)

// Static errors for controller operations.
var (
	ErrVolumeNotFound  = errors.New("volume not found")
	ErrDatasetNotFound = errors.New("dataset not found for share")
)

// mountpointToDatasetID converts a ZFS mountpoint to a dataset ID.
// ZFS datasets are mounted at /mnt/<dataset_name>, so we strip the /mnt/ prefix.
// Example: /mnt/tank/csi/pvc-xxx -> tank/csi/pvc-xxx.
func mountpointToDatasetID(mountpoint string) string {
	return strings.TrimPrefix(mountpoint, "/mnt/")
}

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
	apiClient    tnsapi.ClientInterface
	nodeRegistry *NodeRegistry
}

// NewControllerService creates a new controller service.
func NewControllerService(apiClient tnsapi.ClientInterface, nodeRegistry *NodeRegistry) *ControllerService {
	return &ControllerService{
		apiClient:    apiClient,
		nodeRegistry: nodeRegistry,
	}
}

// lookupVolumeByCSIName finds a volume by its CSI volume name using ZFS properties.
// This is the preferred method for volume discovery as it uses the source of truth (ZFS properties).
// Returns nil, nil if volume not found; returns error only on API failures.
func (s *ControllerService) lookupVolumeByCSIName(ctx context.Context, poolDatasetPrefix, volumeName string) (*VolumeMetadata, error) {
	klog.V(4).Infof("Looking up volume by CSI name: %s (prefix: %s)", volumeName, poolDatasetPrefix)

	dataset, err := s.apiClient.FindDatasetByCSIVolumeName(ctx, poolDatasetPrefix, volumeName)
	if err != nil {
		return nil, fmt.Errorf("failed to find dataset by CSI volume name: %w", err)
	}
	if dataset == nil {
		klog.V(4).Infof("Volume not found by CSI name: %s", volumeName)
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil result
	}

	// Extract metadata from ZFS properties
	props := dataset.UserProperties
	if props == nil {
		klog.Warningf("Dataset %s has no user properties, may not be managed by tns-csi", dataset.ID)
		return nil, nil //nolint:nilnil // Dataset exists but no properties - treat as not found
	}

	// Verify ownership
	if managedBy, ok := props[tnsapi.PropertyManagedBy]; !ok || managedBy.Value != tnsapi.ManagedByValue {
		klog.Warningf("Dataset %s not managed by tns-csi (managed_by=%v)", dataset.ID, props[tnsapi.PropertyManagedBy])
		return nil, nil //nolint:nilnil // Not our volume - treat as not found
	}

	// Build VolumeMetadata from properties
	meta := &VolumeMetadata{
		Name:        volumeName,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
	}

	// Extract protocol
	if protocol, ok := props[tnsapi.PropertyProtocol]; ok {
		meta.Protocol = protocol.Value
	}

	// Extract protocol-specific IDs
	if nfsShareID, ok := props[tnsapi.PropertyNFSShareID]; ok {
		meta.NFSShareID = tnsapi.StringToInt(nfsShareID.Value)
	}
	if nvmeSubsystemID, ok := props[tnsapi.PropertyNVMeSubsystemID]; ok {
		meta.NVMeOFSubsystemID = tnsapi.StringToInt(nvmeSubsystemID.Value)
	}
	if nvmeNamespaceID, ok := props[tnsapi.PropertyNVMeNamespaceID]; ok {
		meta.NVMeOFNamespaceID = tnsapi.StringToInt(nvmeNamespaceID.Value)
	}
	if nvmeNQN, ok := props[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		meta.NVMeOFNQN = nvmeNQN.Value
	}

	klog.V(4).Infof("Found volume: %s (dataset=%s, protocol=%s)", volumeName, dataset.ID, meta.Protocol)
	return meta, nil
}

// lookupSnapshotByCSIName finds a detached snapshot by its CSI snapshot name using ZFS properties.
// This searches for datasets with PropertySnapshotID matching the given name.
// Note: This only finds detached snapshots (stored as datasets). Regular ZFS snapshots
// store properties differently and should be queried via QuerySnapshots.
// Returns nil, nil if snapshot not found; returns error only on API failures.
func (s *ControllerService) lookupSnapshotByCSIName(ctx context.Context, poolDatasetPrefix, snapshotName string) (*SnapshotMetadata, error) {
	klog.Infof("Looking up detached snapshot by property %s=%s (prefix: %q)", tnsapi.PropertySnapshotID, snapshotName, poolDatasetPrefix)

	// Search for datasets with matching snapshot ID property
	datasets, err := s.apiClient.FindDatasetsByProperty(ctx, poolDatasetPrefix, tnsapi.PropertySnapshotID, snapshotName)
	if err != nil {
		klog.Errorf("FindDatasetsByProperty failed for snapshot lookup: %v", err)
		return nil, fmt.Errorf("failed to find snapshot by CSI name: %w", err)
	}

	klog.V(4).Infof("FindDatasetsByProperty returned %d datasets for snapshot_id=%s", len(datasets), snapshotName)

	if len(datasets) == 0 {
		klog.Warningf("Detached snapshot not found by property: %s=%s (no datasets matched)", tnsapi.PropertySnapshotID, snapshotName)
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil result
	}

	if len(datasets) > 1 {
		klog.Warningf("Found multiple datasets with snapshot ID %s (using first): %d datasets", snapshotName, len(datasets))
	}

	dataset := datasets[0]
	props := dataset.UserProperties

	// Verify ownership
	if managedBy, ok := props[tnsapi.PropertyManagedBy]; !ok || managedBy.Value != tnsapi.ManagedByValue {
		klog.Warningf("Snapshot dataset %s not managed by tns-csi", dataset.ID)
		return nil, nil //nolint:nilnil // Not our snapshot - treat as not found
	}

	// Build SnapshotMetadata from properties (uses existing struct from controller_snapshot.go)
	meta := &SnapshotMetadata{
		SnapshotName: snapshotName, // CSI snapshot name
		DatasetName:  dataset.ID,   // Dataset ID where snapshot data lives
	}

	// Extract properties
	if protocol, ok := props[tnsapi.PropertyProtocol]; ok {
		meta.Protocol = protocol.Value
	}
	if sourceVolumeID, ok := props[tnsapi.PropertySourceVolumeID]; ok {
		meta.SourceVolume = sourceVolumeID.Value
	}
	if detached, ok := props[tnsapi.PropertyDetachedSnapshot]; ok {
		meta.Detached = detached.Value == VolumeContextValueTrue
	}

	klog.V(4).Infof("Found snapshot: %s (dataset=%s, protocol=%s, detached=%v)", snapshotName, dataset.ID, meta.Protocol, meta.Detached)
	return meta, nil
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

	// Check for adoption: if volume exists elsewhere (different parentDataset) and can be adopted
	if resp, adopted, err := s.checkAndAdoptVolume(ctx, req, params, protocol); adopted {
		if err != nil {
			return nil, err
		}
		klog.Infof("Successfully adopted orphaned volume: %s", req.GetName())
		return resp, nil
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
		server = "defaultServerAddress" // Default for testing
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

	// Parse pipe separator format: "volume-name | Capacity: 1073741824"
	parts := strings.Split(comment, " | Capacity: ")
	if len(parts) != 2 {
		klog.V(4).Infof("Comment does not match expected format: %s", comment)
		return 0
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

	// Try property-based lookup first (preferred method - uses ZFS properties as source of truth)
	// Pass empty prefix to search all datasets across all pools
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, "", volumeID)
	if err != nil {
		klog.Errorf("Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		// Volume not found - return success per CSI spec (idempotent delete)
		klog.V(4).Infof("Volume %s not found, returning success (idempotent)", volumeID)
		return &csi.DeleteVolumeResponse{}, nil
	}

	klog.V(4).Infof("Found volume %s via property lookup: dataset=%s, protocol=%s", volumeID, volumeMeta.DatasetID, volumeMeta.Protocol)
	switch volumeMeta.Protocol {
	case ProtocolNFS:
		return s.deleteNFSVolume(ctx, volumeMeta)
	case ProtocolNVMeOF:
		return s.deleteNVMeOFVolume(ctx, volumeMeta)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
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
				if strings.Contains(ns.GetDevice(), volumeID) {
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
		devicePath := ns.GetDevice()
		datasets, err := s.apiClient.QueryAllDatasets(ctx, devicePath)
		if err != nil || len(datasets) == 0 {
			klog.V(5).Infof("Skipping NVMe-oF namespace with no matching ZVOL: %s", devicePath)
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

// ========================================
// Volume Adoption Foundation
// ========================================
// These functions provide the foundation for cross-cluster volume adoption.
// A volume is "adoptable" if it has tns-csi metadata but its TrueNAS resources
// (NFS share or NVMe-oF namespace) no longer exist.

// IsVolumeAdoptable checks if a volume can be adopted based on its ZFS properties.
// A volume is adoptable if:
// 1. It has the managed_by property set to tns-csi
// 2. It has a valid schema version
// 3. It has the required protocol-specific properties
// Returns false if the volume doesn't have proper tns-csi metadata.
func IsVolumeAdoptable(props map[string]tnsapi.UserProperty) bool {
	// Check managed_by property
	managedBy, ok := props[tnsapi.PropertyManagedBy]
	if !ok || managedBy.Value != tnsapi.ManagedByValue {
		return false
	}

	// Check schema version (optional for v1, but good practice)
	schemaVersion, hasSchema := props[tnsapi.PropertySchemaVersion]
	if hasSchema && schemaVersion.Value != tnsapi.SchemaVersionV1 {
		// Unknown schema version - don't adopt
		return false
	}

	// Check protocol is set
	protocol, ok := props[tnsapi.PropertyProtocol]
	if !ok || protocol.Value == "" {
		return false
	}

	// Verify protocol-specific required properties exist
	switch protocol.Value {
	case tnsapi.ProtocolNFS:
		// NFS requires share path
		if _, ok := props[tnsapi.PropertyNFSSharePath]; !ok {
			return false
		}
	case tnsapi.ProtocolNVMeOF:
		// NVMe-oF requires NQN
		if _, ok := props[tnsapi.PropertyNVMeSubsystemNQN]; !ok {
			return false
		}
	default:
		// Unknown protocol - don't adopt
		return false
	}

	return true
}

// GetAdoptionInfo extracts adoption-relevant information from volume properties.
// This is useful for building static PV manifests for adopted volumes.
func GetAdoptionInfo(props map[string]tnsapi.UserProperty) map[string]string {
	info := make(map[string]string)

	// Extract core properties
	if v, ok := props[tnsapi.PropertyCSIVolumeName]; ok {
		info["volumeID"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyProtocol]; ok {
		info["protocol"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyCapacityBytes]; ok {
		info["capacityBytes"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyDeleteStrategy]; ok {
		info["deleteStrategy"] = v.Value
	}

	// Extract adoption properties
	if v, ok := props[tnsapi.PropertyPVCName]; ok {
		info["pvcName"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyPVCNamespace]; ok {
		info["pvcNamespace"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyStorageClass]; ok {
		info["storageClass"] = v.Value
	}

	// Extract protocol-specific properties
	if v, ok := props[tnsapi.PropertyNFSSharePath]; ok {
		info["nfsSharePath"] = v.Value
	}
	if v, ok := props[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		info["nvmeofNQN"] = v.Value
	}

	return info
}

// checkAndAdoptVolume searches for an orphaned volume by CSI name and adopts it if eligible.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
// Returns (response, true, nil) if adopted successfully, (nil, true, error) if adoption failed,
// or (nil, false, nil) if no adoptable volume found.
func (s *ControllerService) checkAndAdoptVolume(ctx context.Context, req *csi.CreateVolumeRequest, params map[string]string, protocol string) (*csi.CreateVolumeResponse, bool, error) {
	volumeName := req.GetName()
	adoptExisting := params["adoptExisting"] == VolumeContextValueTrue

	klog.V(4).Infof("Checking for adoptable volume: %s (adoptExisting=%v)", volumeName, adoptExisting)

	// Search for volume by CSI name across ALL pools (empty prefix)
	// This finds volumes even if they exist in a different parentDataset than what's configured
	dataset, err := s.apiClient.FindDatasetByCSIVolumeName(ctx, "", volumeName)
	if err != nil {
		klog.V(4).Infof("Error searching for orphaned volume %s: %v", volumeName, err)
		return nil, false, nil // Not found or error - continue with normal creation
	}
	if dataset == nil {
		klog.V(4).Infof("No orphaned volume found for %s", volumeName)
		return nil, false, nil // Not found - continue with normal creation
	}

	// Found a dataset with matching CSI volume name - check if adoption is allowed
	props := dataset.UserProperties
	if props == nil {
		klog.V(4).Infof("Dataset %s has no user properties, cannot adopt", dataset.ID)
		return nil, false, nil
	}

	// Verify it's managed by tns-csi
	if !IsVolumeAdoptable(props) {
		klog.V(4).Infof("Dataset %s is not adoptable (missing required properties)", dataset.ID)
		return nil, false, nil
	}

	// Check if adoption is allowed: either volume has adoptable=true OR StorageClass has adoptExisting=true
	volumeAdoptable := false
	if adoptableProp, ok := props[tnsapi.PropertyAdoptable]; ok && adoptableProp.Value == "true" {
		volumeAdoptable = true
	}

	if !volumeAdoptable && !adoptExisting {
		klog.V(4).Infof("Volume %s found but adoption not allowed (adoptable=%v, adoptExisting=%v)",
			volumeName, volumeAdoptable, adoptExisting)
		return nil, false, nil
	}

	// Verify protocol matches
	volumeProtocol := ""
	if protocolProp, ok := props[tnsapi.PropertyProtocol]; ok {
		volumeProtocol = protocolProp.Value
	}
	if volumeProtocol != protocol {
		klog.Warningf("Cannot adopt volume %s: protocol mismatch (volume=%s, requested=%s)",
			volumeName, volumeProtocol, protocol)
		return nil, true, status.Errorf(codes.FailedPrecondition,
			"Cannot adopt volume %s: protocol mismatch (volume has %s, requested %s)",
			volumeName, volumeProtocol, protocol)
	}

	klog.Infof("Found adoptable volume %s (dataset=%s, protocol=%s, adoptable=%v, adoptExisting=%v)",
		volumeName, dataset.ID, volumeProtocol, volumeAdoptable, adoptExisting)

	// Handle capacity: expand if requested is larger than existing
	existingCapacity := int64(0)
	if capacityProp, ok := props[tnsapi.PropertyCapacityBytes]; ok {
		existingCapacity = tnsapi.StringToInt64(capacityProp.Value)
	}
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	if requestedCapacity > existingCapacity && existingCapacity > 0 {
		klog.Infof("Expanding adopted volume %s from %d to %d bytes", volumeName, existingCapacity, requestedCapacity)
		if expandErr := s.expandAdoptedVolume(ctx, dataset, protocol, requestedCapacity); expandErr != nil {
			return nil, true, status.Errorf(codes.Internal,
				"Failed to expand adopted volume %s: %v", volumeName, expandErr)
		}
	}

	// Adopt the volume: re-create missing TrueNAS resources based on protocol
	switch protocol {
	case ProtocolNFS:
		resp, err := s.adoptNFSVolume(ctx, req, dataset, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	case ProtocolNVMeOF:
		resp, err := s.adoptNVMeOFVolume(ctx, req, dataset, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	default:
		return nil, true, status.Errorf(codes.InvalidArgument,
			"Unsupported protocol for adoption: %s", protocol)
	}
}

// expandAdoptedVolume expands a volume during adoption if requested capacity is larger.
func (s *ControllerService) expandAdoptedVolume(ctx context.Context, dataset *tnsapi.DatasetWithProperties, protocol string, newCapacityBytes int64) error {
	updateParams := tnsapi.DatasetUpdateParams{}

	switch protocol {
	case ProtocolNFS:
		// NFS uses quota
		updateParams.Quota = &newCapacityBytes
	case ProtocolNVMeOF:
		// NVMe-oF uses volsize
		updateParams.Volsize = &newCapacityBytes
	}

	_, err := s.apiClient.UpdateDataset(ctx, dataset.ID, updateParams)
	if err != nil {
		return fmt.Errorf("failed to expand dataset %s: %w", dataset.ID, err)
	}

	// Update capacity property
	capacityProps := map[string]string{
		tnsapi.PropertyCapacityBytes: strconv.FormatInt(newCapacityBytes, 10),
	}
	if propErr := s.apiClient.SetDatasetProperties(ctx, dataset.ID, capacityProps); propErr != nil {
		klog.Warningf("Failed to update capacity property on %s: %v", dataset.ID, propErr)
	}

	return nil
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
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_GET_VOLUME,
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

	klog.Infof("ControllerExpandVolume: Expanding volume %s to %d bytes", volumeID, requiredBytes)

	// Look up volume using ZFS properties as source of truth
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, "", volumeID)
	if err != nil {
		klog.Errorf("ControllerExpandVolume: Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		klog.Errorf("ControllerExpandVolume: Volume %s not found", volumeID)
		return nil, status.Errorf(codes.NotFound, "Volume %s not found for expansion", volumeID)
	}

	klog.V(4).Infof("ControllerExpandVolume: Found volume %s via property lookup: dataset=%s, protocol=%s", volumeID, volumeMeta.DatasetID, volumeMeta.Protocol)
	switch volumeMeta.Protocol {
	case ProtocolNFS:
		klog.Infof("Expanding NFS volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandNFSVolume(ctx, volumeMeta, requiredBytes)
	case ProtocolNVMeOF:
		klog.Infof("Expanding NVMe-oF volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandNVMeOFVolume(ctx, volumeMeta, requiredBytes)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
}

// ControllerGetVolume returns volume information including health status.
// This is used by Kubernetes to monitor volume health and report conditions.
// Per CSI spec, this returns VolumeCondition with Abnormal flag and Message.
func (s *ControllerService) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("ControllerGetVolume called with request: %+v", req)

	// Validate request
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("Getting volume info for: %s", volumeID)

	// Look up volume using ZFS properties as source of truth
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, "", volumeID)
	if err != nil {
		klog.Errorf("ControllerGetVolume: Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		klog.V(4).Infof("Volume %s not found", volumeID)
		return nil, status.Errorf(codes.NotFound, "Volume %s not found", volumeID)
	}

	switch volumeMeta.Protocol {
	case ProtocolNFS:
		return s.getNFSVolumeInfo(ctx, volumeMeta)
	case ProtocolNVMeOF:
		return s.getNVMeOFVolumeInfo(ctx, volumeMeta)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
}

// getNFSVolumeInfo retrieves volume information and health status for an NFS volume.
func (s *ControllerService) getNFSVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting NFS volume info: %s (dataset: %s, shareID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	abnormal := false
	var messages []string

	// Check 1: Verify dataset exists
	dataset, err := s.apiClient.Dataset(ctx, meta.DatasetName)
	if err != nil || dataset == nil {
		abnormal = true
		messages = append(messages, fmt.Sprintf("Dataset %s not accessible: %v", meta.DatasetName, err))
	} else {
		klog.V(4).Infof("Dataset %s exists (ID: %s)", meta.DatasetName, dataset.ID)
	}

	// Check 2: Verify NFS share exists and is enabled
	if meta.NFSShareID > 0 {
		shares, err := s.apiClient.QueryAllNFSShares(ctx, "")
		if err != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query NFS shares: %v", err))
		} else {
			// Find the share by ID
			var foundShare *tnsapi.NFSShare
			for i := range shares {
				if shares[i].ID == meta.NFSShareID {
					foundShare = &shares[i]
					break
				}
			}
			switch {
			case foundShare == nil:
				abnormal = true
				messages = append(messages, fmt.Sprintf("NFS share %d not found", meta.NFSShareID))
			case !foundShare.Enabled:
				abnormal = true
				messages = append(messages, fmt.Sprintf("NFS share %d is disabled", meta.NFSShareID))
			default:
				klog.V(4).Infof("NFS share %d is healthy (enabled: %t, path: %s)", foundShare.ID, foundShare.Enabled, foundShare.Path)
			}
		}
	}

	// Build response message
	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	// Build volume context
	volumeContext := buildVolumeContext(*meta)

	// Get capacity from dataset if available
	var capacityBytes int64
	if dataset != nil && dataset.Available != nil {
		if val, ok := dataset.Available["parsed"].(float64); ok {
			capacityBytes = int64(val)
		}
	}

	klog.V(4).Infof("NFS volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: volumeContext,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

// getNVMeOFVolumeInfo retrieves volume information and health status for an NVMe-oF volume.
func (s *ControllerService) getNVMeOFVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting NVMe-oF volume info: %s (dataset: %s, subsystemID: %d, namespaceID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFSubsystemID, meta.NVMeOFNamespaceID)

	abnormal := false
	var messages []string

	// Check 1: Verify ZVOL exists
	var datasets []tnsapi.Dataset
	datasets, err := s.apiClient.QueryAllDatasets(ctx, meta.DatasetName)
	switch {
	case err != nil:
		abnormal = true
		messages = append(messages, fmt.Sprintf("ZVOL %s query failed: %v", meta.DatasetName, err))
	case len(datasets) == 0:
		abnormal = true
		messages = append(messages, fmt.Sprintf("ZVOL %s not found", meta.DatasetName))
	default:
		klog.V(4).Infof("ZVOL %s exists (ID: %s)", meta.DatasetName, datasets[0].ID)
	}

	// Check 2: Verify NVMe-oF subsystem exists
	var subsystemHealthy bool
	if meta.NVMeOFSubsystemID > 0 {
		subsystems, err := s.apiClient.ListAllNVMeOFSubsystems(ctx)
		if err != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query NVMe-oF subsystems: %v", err))
		} else {
			// Find the subsystem by ID
			var foundSubsystem *tnsapi.NVMeOFSubsystem
			for i := range subsystems {
				if subsystems[i].ID == meta.NVMeOFSubsystemID {
					foundSubsystem = &subsystems[i]
					break
				}
			}
			if foundSubsystem == nil {
				abnormal = true
				messages = append(messages, fmt.Sprintf("NVMe-oF subsystem %d not found", meta.NVMeOFSubsystemID))
			} else {
				subsystemHealthy = true
				klog.V(4).Infof("NVMe-oF subsystem %d is healthy (NQN: %s)", foundSubsystem.ID, foundSubsystem.NQN)
			}
		}
	}

	// Check 3: Verify NVMe-oF namespace exists
	if meta.NVMeOFNamespaceID > 0 && subsystemHealthy {
		namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
		if err != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query NVMe-oF namespaces: %v", err))
		} else {
			// Find the namespace by ID
			var foundNamespace *tnsapi.NVMeOFNamespace
			for i := range namespaces {
				if namespaces[i].ID == meta.NVMeOFNamespaceID {
					foundNamespace = &namespaces[i]
					break
				}
			}
			if foundNamespace == nil {
				abnormal = true
				messages = append(messages, fmt.Sprintf("NVMe-oF namespace %d not found", meta.NVMeOFNamespaceID))
			} else {
				klog.V(4).Infof("NVMe-oF namespace %d is healthy (NSID: %d, device: %s)",
					foundNamespace.ID, foundNamespace.NSID, foundNamespace.GetDevice())
			}
		}
	}

	// Build response message
	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	// Build volume context
	volumeContext := buildVolumeContext(*meta)

	// Get capacity from ZVOL if available
	var capacityBytes int64
	if len(datasets) > 0 {
		capacityBytes = getZvolCapacity(&datasets[0])
	}

	klog.V(4).Infof("NVMe-oF volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: volumeContext,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

// ControllerModifyVolume modifies a volume.
func (s *ControllerService) ControllerModifyVolume(_ context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	klog.V(4).Infof("ControllerModifyVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}
