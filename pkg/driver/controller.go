// Package driver implements the CSI driver controller service.
package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// APIClient defines the interface for TrueNAS API operations.
//
//nolint:dupl // Interface and mock struct have similar structure by design
type APIClient interface {
	CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	DeleteDataset(ctx context.Context, datasetID string) error
	CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShare(ctx context.Context, shareID int) error
	CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)
	CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error
	CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error
}

// VolumeMetadata contains information needed to manage a volume.
type VolumeMetadata struct {
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	DatasetID         string `json:"datasetID,omitempty"`
	DatasetName       string `json:"datasetName,omitempty"`
	NFSShareID        int    `json:"nfsShareID,omitempty"`
	NVMeOFSubsystemID int    `json:"nvmeofSubsystemID,omitempty"`
	NVMeOFNamespaceID int    `json:"nvmeofNamespaceID,omitempty"`
	NVMeOFNQN         string `json:"nvmeofNQN,omitempty"`
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
		return nil, fmt.Errorf("volume ID is not in encoded format")
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
	// Try to decode it to verify it's valid base64
	_, err := base64.RawURLEncoding.DecodeString(volumeID)
	return err == nil
}

// ControllerService implements the CSI Controller service.
type ControllerService struct {
	csi.UnimplementedControllerServer
	apiClient APIClient
}

// NewControllerService creates a new controller service.
func NewControllerService(apiClient APIClient) *ControllerService {
	return &ControllerService{
		apiClient: apiClient,
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

	klog.Infof("Creating volume %s with protocol %s", req.GetName(), protocol)

	// TODO: Implement volume creation based on protocol
	switch protocol {
	case ProtocolNFS:
		return s.createNFSVolume(ctx, req)
	case ProtocolISCSI:
		return s.createISCSIVolume(ctx, req)
	case ProtocolNVMeOF:
		return s.createNVMeOFVolume(ctx, req)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported protocol: %s", protocol)
	}
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
	case ProtocolISCSI:
		return s.deleteISCSIVolume(ctx, meta)
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

	// For NFS, this is typically a no-op
	// For iSCSI/NVMe-oF, we would configure access here
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerService) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called with request: %+v", req)

	// For NFS, this is typically a no-op
	// For iSCSI/NVMe-oF, we would remove access here
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

	// TODO: Implement actual validation
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// ListVolumes lists all volumes.
func (s *ControllerService) ListVolumes(_ context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Infof("ListVolumes called with request: %+v", req)

	// TODO: Implement volume listing
	return &csi.ListVolumesResponse{}, nil
}

// GetCapacity returns the capacity of the storage pool.
func (s *ControllerService) GetCapacity(_ context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Infof("GetCapacity called with request: %+v", req)

	// TODO: Implement capacity reporting
	return &csi.GetCapacityResponse{}, nil
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
		},
	}, nil
}

// CreateSnapshot creates a volume snapshot.
func (s *ControllerService) CreateSnapshot(_ context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	klog.V(4).Infof("CreateSnapshot called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "CreateSnapshot not implemented")
}

// DeleteSnapshot deletes a snapshot.
func (s *ControllerService) DeleteSnapshot(_ context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	klog.V(4).Infof("DeleteSnapshot called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "DeleteSnapshot not implemented")
}

// ListSnapshots lists snapshots.
func (s *ControllerService) ListSnapshots(_ context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "ListSnapshots not implemented")
}

// ControllerExpandVolume expands a volume.
func (s *ControllerService) ControllerExpandVolume(_ context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).Infof("ControllerExpandVolume called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "ControllerExpandVolume not implemented")
}

// ControllerGetVolume gets volume information.
func (s *ControllerService) ControllerGetVolume(_ context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("ControllerGetVolume called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume not implemented")
}

// ControllerModifyVolume modifies a volume.
func (s *ControllerService) ControllerModifyVolume(_ context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	klog.V(4).Infof("ControllerModifyVolume called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}

// Protocol-specific volume creation methods

func (s *ControllerService) createNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating NFS volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NFS volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NFS volumes")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Construct dataset name (parent/volumeID)
	volumeID := req.GetName()
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeID)

	klog.Infof("Creating dataset: %s", datasetName)

	// Step 1: Create ZFS dataset
	dataset, err := s.apiClient.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: datasetName,
		Type: "FILESYSTEM",
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create dataset: %v", err)
	}

	klog.Infof("Created dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)

	// Step 2: Create NFS share for the dataset
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      fmt.Sprintf("CSI Volume: %s", volumeID),
		MaprootUser:  "root",
		MaprootGroup: "wheel",
		Enabled:      true,
	})
	if err != nil {
		// Cleanup: delete the dataset if NFS share creation fails
		klog.Errorf("Failed to create NFS share, cleaning up dataset: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset after NFS share creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share: %v", err)
	}

	klog.Infof("Created NFS share with ID: %d for path: %s", nfsShare.ID, nfsShare.Path)

	// Encode volume metadata into volumeID
	volumeName := req.GetName()
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		NFSShareID:  nfsShare.ID,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete NFS share and dataset
		klog.Errorf("Failed to encode volume ID, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNFSShare(ctx, nfsShare.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NFS share: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID: %v", err)
	}

	// Construct volume context with metadata for node plugin
	volumeContext := map[string]string{
		"server":      server,
		"share":       dataset.Mountpoint,
		"datasetID":   dataset.ID,
		"datasetName": dataset.Name,
		"nfsShareID":  fmt.Sprintf("%d", nfsShare.ID),
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	klog.Infof("Successfully created NFS volume with encoded ID: %s", encodedVolumeID)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

func (s *ControllerService) createISCSIVolume(_ context.Context, _ *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating iSCSI volume")

	// TODO: Implement iSCSI volume creation
	// 1. Create zvol
	// 2. Create iSCSI extent
	// 3. Create target
	// 4. Associate extent with target

	return nil, status.Error(codes.Unimplemented, "iSCSI volume creation not yet implemented")
}

func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating NVMe-oF volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NVMe-oF volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Construct ZVOL name (parent/volumeID)
	volumeName := req.GetName()
	zvolName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	klog.Infof("Creating ZVOL: %s with size: %d bytes", zvolName, requestedCapacity)

	// Step 1: Create ZVOL (block device)
	zvol, err := s.apiClient.CreateZvol(ctx, tnsapi.ZvolCreateParams{
		Name:         zvolName,
		Type:         "VOLUME",
		Volsize:      requestedCapacity,
		Volblocksize: "16K", // Default block size for NVMe-oF
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", err)
	}

	klog.Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)

	klog.Infof("Creating NVMe-oF subsystem with name: %s", volumeName)

	// Step 2: Create NVMe-oF subsystem
	// Use volume name as the subsystem name (Storage will auto-generate NQN)
	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
		Name: volumeName,
	})
	if err != nil {
		// Cleanup: delete the ZVOL if subsystem creation fails
		klog.Errorf("Failed to create NVMe-oF subsystem, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL after subsystem creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem: %v", err)
	}

	klog.Infof("Created NVMe-oF subsystem with ID: %d", subsystem.ID)

	// Step 3: Create NVMe-oF namespace
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := fmt.Sprintf("zvol/%s", zvolName)

	klog.Infof("Creating NVMe-oF namespace for device: %s", devicePath)

	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		NSID:       1, // Start with namespace ID 1
	})
	if err != nil {
		// Cleanup: delete subsystem and ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.Infof("Created NVMe-oF namespace with ID: %d (NSID: %d)", namespace.ID, namespace.NSID)

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          "nvmeof",
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete namespace, subsystem, and ZVOL
		klog.Errorf("Failed to encode volume ID, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespace.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID: %v", err)
	}

	// Construct volume context with metadata for node plugin
	volumeContext := map[string]string{
		"server":            server,
		"nqn":               subsystem.NQN,
		"datasetID":         zvol.ID,
		"datasetName":       zvol.Name,
		"nvmeofSubsystemID": fmt.Sprintf("%d", subsystem.ID),
		"nvmeofNamespaceID": fmt.Sprintf("%d", namespace.ID),
		"nsid":              fmt.Sprintf("%d", namespace.NSID),
	}

	klog.Infof("Successfully created NVMe-oF volume with encoded ID: %s", encodedVolumeID)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// deleteNFSVolume deletes an NFS volume.
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("Deleting NFS volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	// Step 1: Delete NFS share
	if meta.NFSShareID > 0 {
		klog.Infof("Deleting NFS share with ID: %d", meta.NFSShareID)
		if err := s.apiClient.DeleteNFSShare(ctx, meta.NFSShareID); err != nil {
			// Log error but continue - the share might already be deleted
			klog.Warningf("Failed to delete NFS share %d: %v (continuing anyway)", meta.NFSShareID, err)
		} else {
			klog.Infof("Successfully deleted NFS share %d", meta.NFSShareID)
		}
	}

	// Step 2: Delete ZFS dataset
	if meta.DatasetID != "" {
		klog.Infof("Deleting dataset: %s", meta.DatasetID)
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			// Check if dataset doesn't exist - this is OK (idempotency)
			klog.Warningf("Failed to delete dataset %s: %v (continuing anyway)", meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted dataset %s", meta.DatasetID)
		}
	}

	klog.Infof("Successfully deleted NFS volume: %s", meta.Name)
	return &csi.DeleteVolumeResponse{}, nil
}

// deleteISCSIVolume deletes an iSCSI volume.
func (s *ControllerService) deleteISCSIVolume(_ context.Context, _ *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Info("Deleting iSCSI volume")

	// TODO: Implement iSCSI volume deletion
	// 1. Remove target associations
	// 2. Delete iSCSI extent
	// 3. Delete zvol

	return nil, status.Error(codes.Unimplemented, "iSCSI volume deletion not yet implemented")
}

// deleteNVMeOFVolume deletes an NVMe-oF volume.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, subsystem ID: %d, namespace ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFSubsystemID, meta.NVMeOFNamespaceID)

	// Step 1: Delete NVMe-oF namespace
	if meta.NVMeOFNamespaceID > 0 {
		klog.Infof("Deleting NVMe-oF namespace with ID: %d", meta.NVMeOFNamespaceID)
		if err := s.apiClient.DeleteNVMeOFNamespace(ctx, meta.NVMeOFNamespaceID); err != nil {
			// Log error but continue - the namespace might already be deleted
			klog.Warningf("Failed to delete NVMe-oF namespace %d: %v (continuing anyway)", meta.NVMeOFNamespaceID, err)
		} else {
			klog.Infof("Successfully deleted NVMe-oF namespace %d", meta.NVMeOFNamespaceID)
		}
	}

	// Step 2: Delete NVMe-oF subsystem
	if meta.NVMeOFSubsystemID > 0 {
		klog.Infof("Deleting NVMe-oF subsystem with ID: %d", meta.NVMeOFSubsystemID)
		if err := s.apiClient.DeleteNVMeOFSubsystem(ctx, meta.NVMeOFSubsystemID); err != nil {
			// Log error but continue - the subsystem might already be deleted
			klog.Warningf("Failed to delete NVMe-oF subsystem %d: %v (continuing anyway)", meta.NVMeOFSubsystemID, err)
		} else {
			klog.Infof("Successfully deleted NVMe-oF subsystem %d", meta.NVMeOFSubsystemID)
		}
	}

	// Step 3: Delete ZVOL
	if meta.DatasetID != "" {
		klog.Infof("Deleting ZVOL: %s", meta.DatasetID)
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			// Check if dataset doesn't exist - this is OK (idempotency)
			klog.Warningf("Failed to delete ZVOL %s: %v (continuing anyway)", meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted ZVOL %s", meta.DatasetID)
		}
	}

	klog.Infof("Successfully deleted NVMe-oF volume: %s", meta.Name)
	return &csi.DeleteVolumeResponse{}, nil
}
