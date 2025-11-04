package driver

import (
	"context"
	"os"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Protocol constants.
const (
	ProtocolNFS    = "nfs"
	ProtocolNVMeOF = "nvmeof"
	ProtocolISCSI  = "iscsi"
)

// NodeService implements the CSI Node service.
type NodeService struct {
	csi.UnimplementedNodeServer
	apiClient *tnsapi.Client
	nodeID    string
}

// NewNodeService creates a new node service.
func NewNodeService(nodeID string, apiClient *tnsapi.Client) *NodeService {
	return &NodeService{
		nodeID:    nodeID,
		apiClient: apiClient,
	}
}

// NodeStageVolume stages a volume to a staging path.
func (s *NodeService) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.V(4).Infof("NodeStageVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeContext := req.GetVolumeContext()

	// Decode volume metadata to determine protocol
	meta, err := decodeVolumeID(volumeID)
	if err != nil {
		klog.Warningf("Failed to decode volume ID %s: %v, checking volume context for protocol", volumeID, err)
		// Try to determine protocol from volume context if metadata decode fails
		// This handles backwards compatibility
		if nqn := volumeContext["nqn"]; nqn != "" {
			return s.stageNVMeOFVolume(ctx, req, volumeContext)
		}
		// Default to NFS for backwards compatibility
		klog.V(4).Infof("Volume appears to be NFS (no staging required)")
		return &csi.NodeStageVolumeResponse{}, nil
	}

	klog.Infof("Staging volume %s (protocol: %s) to %s", meta.Name, meta.Protocol, stagingTargetPath)

	// Stage volume based on protocol
	switch meta.Protocol {
	case ProtocolNFS:
		// NFS volumes don't need staging - mounting happens in NodePublishVolume
		klog.V(4).Infof("NFS volume, no staging required")
		return &csi.NodeStageVolumeResponse{}, nil

	case ProtocolNVMeOF:
		return s.stageNVMeOFVolume(ctx, req, volumeContext)

	case ProtocolISCSI:
		return nil, status.Error(codes.Unimplemented, "iSCSI staging not yet implemented")

	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol: %s", meta.Protocol)
	}
}

// NodeUnstageVolume unstages a volume from a staging path.
func (s *NodeService) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	klog.V(4).Infof("NodeUnstageVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}

	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	// Decode volume metadata to determine protocol
	meta, err := decodeVolumeID(volumeID)
	if err != nil {
		klog.Warningf("Failed to decode volume ID %s: %v, attempting unstage anyway", volumeID, err)
		// Try to unmount staging path if it exists
		mounted, err := mount.IsMounted(ctx, stagingTargetPath)
		if err != nil {
			klog.Warningf("Failed to check if staging path is mounted: %v", err)
		}
		if mounted {
			if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
				klog.Warningf("Failed to unmount staging path: %v", err)
			}
		}
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	klog.Infof("Unstaging volume %s (protocol: %s) from %s", meta.Name, meta.Protocol, stagingTargetPath)

	// Unstage volume based on protocol
	switch meta.Protocol {
	case ProtocolNFS:
		// NFS volumes don't need unstaging
		klog.V(4).Infof("NFS volume, no unstaging required")
		return &csi.NodeUnstageVolumeResponse{}, nil

	case ProtocolNVMeOF:
		// For unstageNVMeOFVolume, we need volume context but don't have it in UnstageVolume request
		// We'll use metadata from volumeID instead
		volumeContext := map[string]string{
			"nqn": meta.NVMeOFNQN,
		}
		return s.unstageNVMeOFVolume(ctx, req, volumeContext)

	case ProtocolISCSI:
		return nil, status.Error(codes.Unimplemented, "iSCSI unstaging not yet implemented")

	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol: %s", meta.Protocol)
	}
}

// NodePublishVolume mounts the volume to the target path.
func (s *NodeService) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.V(4).Infof("NodePublishVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path is required")
	}

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	// Determine protocol from volume metadata or context
	meta, err := decodeVolumeID(volumeID)
	if err != nil {
		klog.Warningf("Failed to decode volume ID %s: %v, assuming NFS", volumeID, err)
		// Fall back to NFS behavior for backwards compatibility
		return s.publishNFSVolume(ctx, req)
	}

	klog.Infof("Publishing volume %s (protocol: %s) to %s", meta.Name, meta.Protocol, targetPath)

	// Publish volume based on protocol
	switch meta.Protocol {
	case ProtocolNFS:
		return s.publishNFSVolume(ctx, req)

	case ProtocolNVMeOF:
		// For block volumes with staging, this is a bind mount from staging to target
		stagingTargetPath := req.GetStagingTargetPath()
		if stagingTargetPath == "" {
			return nil, status.Error(codes.InvalidArgument, "Staging target path is required for NVMe-oF volumes")
		}
		return s.publishBlockVolume(ctx, stagingTargetPath, targetPath, req.GetReadonly())

	case ProtocolISCSI:
		// Same as NVMe-oF - bind mount from staging to target
		stagingTargetPath := req.GetStagingTargetPath()
		if stagingTargetPath == "" {
			return nil, status.Error(codes.InvalidArgument, "Staging target path is required for iSCSI volumes")
		}
		return s.publishBlockVolume(ctx, stagingTargetPath, targetPath, req.GetReadonly())

	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol: %s", meta.Protocol)
	}
}

// NodeUnpublishVolume unmounts the volume from the target path.
func (s *NodeService) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.V(4).Infof("NodeUnpublishVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path is required")
	}

	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	klog.Infof("Unmounting volume %s from %s", volumeID, targetPath)

	// Check if mounted
	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if !mounted {
		klog.V(4).Infof("Path %s is not mounted, nothing to do", targetPath)
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	// Unmount
	klog.V(4).Infof("Executing umount command for: %s", targetPath)
	if err := mount.Unmount(ctx, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount: %v", err)
	}

	klog.Infof("Successfully unmounted volume %s from %s", volumeID, targetPath)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetVolumeStats returns volume capacity statistics.
func (s *NodeService) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	klog.V(4).Infof("NodeGetVolumeStats called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	volumePath := req.GetVolumePath()
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume path is required")
	}

	// Verify the volume path exists
	pathInfo, err := os.Stat(volumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "Volume path %s does not exist", volumePath)
		}
		return nil, status.Errorf(codes.Internal, "Failed to stat volume path: %v", err)
	}

	// Get filesystem statistics
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(volumePath, &statfs); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get volume stats: %v", err)
	}

	// Calculate capacity, used, and available bytes
	// Note: statfs returns values in blocks, need to multiply by block size
	// Use platform-specific helper to safely convert Bsize to uint64
	blockSize := getBlockSize(&statfs)
	totalBytes := statfs.Blocks * blockSize
	availableBytes := statfs.Bavail * blockSize
	usedBytes := totalBytes - (statfs.Bfree * blockSize)

	klog.V(4).Infof("Volume stats for %s: total=%d, used=%d, available=%d",
		volumePath, totalBytes, usedBytes, availableBytes)

	resp := &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Total:     safeUint64ToInt64(totalBytes),
				Used:      safeUint64ToInt64(usedBytes),
				Available: safeUint64ToInt64(availableBytes),
			},
		},
	}

	// For directories (filesystem mounts), also report inode statistics
	if pathInfo.IsDir() {
		totalInodes := statfs.Files
		freeInodes := statfs.Ffree
		usedInodes := totalInodes - freeInodes

		resp.Usage = append(resp.Usage, &csi.VolumeUsage{
			Unit:      csi.VolumeUsage_INODES,
			Total:     safeUint64ToInt64(totalInodes),
			Used:      safeUint64ToInt64(usedInodes),
			Available: safeUint64ToInt64(freeInodes),
		})

		klog.V(4).Infof("Inode stats for %s: total=%d, used=%d, free=%d",
			volumePath, totalInodes, usedInodes, freeInodes)
	}

	return resp, nil
}

// NodeExpandVolume expands a volume.
func (s *NodeService) NodeExpandVolume(_ context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	klog.V(4).Infof("NodeExpandVolume called with request: %+v", req)
	return nil, status.Error(codes.Unimplemented, "NodeExpandVolume not implemented")
}

// NodeGetCapabilities returns node capabilities.
func (s *NodeService) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(4).Info("NodeGetCapabilities called")

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
					},
				},
			},
		},
	}, nil
}

// NodeGetInfo returns node information.
func (s *NodeService) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(4).Info("NodeGetInfo called")

	return &csi.NodeGetInfoResponse{
		NodeId: s.nodeID,
	}, nil
}

// Helper functions

// safeUint64ToInt64 safely converts uint64 to int64, capping at math.MaxInt64.
// This is necessary for CSI VolumeUsage which uses int64 per the protobuf spec.
func safeUint64ToInt64(val uint64) int64 {
	const maxInt64 = 9223372036854775807 // math.MaxInt64
	if val > maxInt64 {
		return maxInt64
	}
	return int64(val)
}
