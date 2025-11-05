// Package driver implements the CSI driver controller service.
package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for controller operations.
var (
	ErrVolumeIDNotEncoded = errors.New("volume ID is not in encoded format")
)

// APIClient defines the interface for TrueNAS API operations.
//
//nolint:interfacebloat // Interface represents cohesive TrueNAS storage API - splitting would reduce clarity
type APIClient interface {
	CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	DeleteDataset(ctx context.Context, datasetID string) error
	UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error)
	CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShare(ctx context.Context, shareID int) error
	CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)
	CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error
	CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error
	QueryNVMeOFPorts(ctx context.Context) ([]tnsapi.NVMeOFPort, error)
	AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error
	CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) error
	QuerySnapshots(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error)
	CloneSnapshot(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error)
}

// VolumeMetadata contains information needed to manage a volume.
type VolumeMetadata struct {
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	DatasetID         string `json:"datasetID,omitempty"`
	DatasetName       string `json:"datasetName,omitempty"`
	NVMeOFNQN         string `json:"nvmeofNQN,omitempty"`
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

	// Check if creating from snapshot
	klog.Infof("Checking VolumeContentSource for volume %s: %+v", req.GetName(), req.GetVolumeContentSource())
	if req.GetVolumeContentSource() != nil {
		klog.Infof("VolumeContentSource is NOT nil for volume %s", req.GetName())
		if snapshot := req.GetVolumeContentSource().GetSnapshot(); snapshot != nil {
			klog.Infof("=== SNAPSHOT RESTORE DETECTED === Creating volume %s from snapshot %s with protocol %s",
				req.GetName(), snapshot.GetSnapshotId(), protocol)
			return s.createVolumeFromSnapshot(ctx, req, snapshot.GetSnapshotId())
		} else {
			klog.Warningf("VolumeContentSource exists but snapshot is nil for volume %s", req.GetName())
		}
	} else {
		klog.V(4).Infof("VolumeContentSource is nil for volume %s (normal volume creation)", req.GetName())
	}

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

	// For NFS and NVMe-oF, this is typically a no-op
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerService) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called with request: %+v", req)

	// For NFS and NVMe-oF, this is typically a no-op
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

	// Basic validation: we accept all requested capabilities since TrueNAS supports both filesystem and block modes
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// ListVolumes lists all volumes.
func (s *ControllerService) ListVolumes(_ context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Infof("ListVolumes called with request: %+v", req)

	// Optional CSI capability - not required for basic functionality
	// Kubernetes does not require this for normal PVC operations
	return &csi.ListVolumesResponse{}, nil
}

// GetCapacity returns the capacity of the storage pool.
func (s *ControllerService) GetCapacity(_ context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Infof("GetCapacity called with request: %+v", req)

	// Optional CSI capability - not required for basic functionality
	// Could query TrueNAS pool capacity in the future if needed
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
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}
