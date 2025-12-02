package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
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
)

// Filesystem type constants.
const (
	fsTypeExt2 = "ext2"
	fsTypeExt3 = "ext3"
	fsTypeExt4 = "ext4"
	fsTypeXFS  = "xfs"
)

// NodeService implements the CSI Node service.
type NodeService struct {
	csi.UnimplementedNodeServer
	apiClient    tnsapi.ClientInterface
	nodeRegistry *NodeRegistry
	nodeID       string
	testMode     bool // Test mode flag to skip actual mounts
}

// NewNodeService creates a new node service.
func NewNodeService(nodeID string, apiClient tnsapi.ClientInterface, testMode bool, nodeRegistry *NodeRegistry) *NodeService {
	return &NodeService{
		nodeID:       nodeID,
		apiClient:    apiClient,
		testMode:     testMode,
		nodeRegistry: nodeRegistry,
	}
}

// NodeStageVolume stages a volume to a staging path.
func (s *NodeService) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("node", "stage")
	klog.V(4).Infof("NodeStageVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetStagingTargetPath() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}

	if req.GetVolumeCapability() == nil {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeContext := req.GetVolumeContext()

	// Determine protocol from VolumeContext
	// With plain volume IDs (just the volume name), all metadata is passed via VolumeContext
	protocol := getProtocolFromVolumeContext(volumeContext)

	klog.V(4).Infof("Staging volume %s (protocol: %s) to %s", volumeID, protocol, stagingTargetPath)

	// Stage volume based on protocol
	switch protocol {
	case ProtocolNFS:
		resp, err := s.stageNFSVolume(ctx, req, volumeContext)
		if err != nil {
			timer.ObserveError()
			return nil, err
		}
		timer.ObserveSuccess()
		return resp, nil

	case ProtocolNVMeOF:
		resp, err := s.stageNVMeOFVolume(ctx, req, volumeContext)
		if err != nil {
			timer.ObserveError()
			return nil, err
		}
		timer.ObserveSuccess()
		return resp, nil

	default:
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported protocol: %s (supported: nfs, nvmeof)", protocol)
	}
}

// NodeUnstageVolume unstages a volume from a staging path.
func (s *NodeService) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("node", "unstage")
	klog.V(4).Infof("NodeUnstageVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetStagingTargetPath() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}

	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	// With independent subsystems, we determine the protocol by checking the staging path
	// NVMe-oF volumes use block devices, NFS volumes use NFS mounts
	// Try to detect the mount type from the staging path
	protocol := s.detectProtocolFromStagingPath(ctx, stagingTargetPath)

	klog.V(4).Infof("Unstaging volume %s (protocol: %s) from %s", volumeID, protocol, stagingTargetPath)

	if protocol == ProtocolNVMeOF {
		// For NVMe-oF, we need to pass the NQN which is derived from the volume ID
		// With independent subsystems, NQN format is: nqn.2024-01.io.truenas.csi:<volumeID>
		volumeContext := map[string]string{
			"nqn": fmt.Sprintf("nqn.2024-01.io.truenas.csi:%s", volumeID),
		}
		resp, err := s.unstageNVMeOFVolume(ctx, req, volumeContext)
		if err != nil {
			timer.ObserveError()
			return nil, err
		}
		timer.ObserveSuccess()
		return resp, nil
	}

	// Default to NFS volume unstaging
	klog.V(4).Infof("Unstaging NFS volume %s from %s", volumeID, stagingTargetPath)
	resp, err := s.unstageNFSVolume(ctx, req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	timer.ObserveSuccess()
	return resp, nil
}

// detectProtocolFromStagingPath attempts to detect whether the staging path is NVMe-oF or NFS.
// It checks the mount source to determine if it's a block device or NFS mount.
func (s *NodeService) detectProtocolFromStagingPath(ctx context.Context, stagingPath string) string {
	// Check if the path exists first
	if _, err := os.Stat(stagingPath); os.IsNotExist(err) {
		// Path doesn't exist, default to NFS (most common case for cleanup)
		return ProtocolNFS
	}

	// Check if it's mounted
	mounted, err := mount.IsMounted(ctx, stagingPath)
	if err != nil || !mounted {
		// Not mounted or error - check if there's an NVMe device symlink
		// For block volumes, the staging path is a symlink to the device
		if info, statErr := os.Lstat(stagingPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				// It's a symlink, likely a block device - NVMe-oF
				return ProtocolNVMeOF
			}
		}
		return ProtocolNFS
	}

	// It's mounted - check the filesystem type using findmnt
	fsType, err := detectFilesystemType(ctx, stagingPath)
	if err != nil {
		// Default to NFS if we can't detect
		return ProtocolNFS
	}

	// NFS mounts will show "nfs" or "nfs4" as filesystem type
	if strings.HasPrefix(fsType, "nfs") {
		return ProtocolNFS
	}

	// ext4, xfs etc. are typically from block devices (NVMe-oF)
	return ProtocolNVMeOF
}

// NodePublishVolume mounts the volume to the target path.
func (s *NodeService) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("node", "publish")
	klog.V(4).Infof("NodePublishVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetTargetPath() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Target path is required")
	}

	if req.GetVolumeCapability() == nil {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	volumeContext := req.GetVolumeContext()

	// Determine protocol from VolumeContext
	protocol := getProtocolFromVolumeContext(volumeContext)

	klog.V(4).Infof("Publishing volume %s (protocol: %s) to %s", volumeID, protocol, targetPath)

	// Publish volume based on protocol
	switch protocol {
	case ProtocolNFS:
		resp, respErr := s.publishNFSVolume(ctx, req)
		if respErr != nil {
			timer.ObserveError()
			return nil, respErr
		}
		timer.ObserveSuccess()
		return resp, nil

	case ProtocolNVMeOF:
		// NVMe-oF supports both block and filesystem volume modes
		stagingTargetPath := req.GetStagingTargetPath()
		if stagingTargetPath == "" {
			timer.ObserveError()
			return nil, status.Error(codes.InvalidArgument, "Staging target path is required for NVMe-oF volumes")
		}

		// Check volume capability to determine how to publish
		var resp *csi.NodePublishVolumeResponse
		var err error
		if req.GetVolumeCapability().GetBlock() != nil {
			// Block volume: staging path is a device file, bind mount it
			resp, err = s.publishBlockVolume(ctx, stagingTargetPath, targetPath, req.GetReadonly())
		} else {
			// Filesystem volume: staging path is a mounted directory, bind mount the directory
			resp, err = s.publishFilesystemVolume(ctx, stagingTargetPath, targetPath, req.GetReadonly())
		}
		if err != nil {
			timer.ObserveError()
			return nil, err
		}
		timer.ObserveSuccess()
		return resp, nil

	default:
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Unknown protocol: %s", protocol)
	}
}

// NodeUnpublishVolume unmounts the volume from the target path.
func (s *NodeService) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("node", "unpublish")
	klog.V(4).Infof("NodeUnpublishVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetTargetPath() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Target path is required")
	}

	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	klog.V(4).Infof("Unmounting volume %s from %s", volumeID, targetPath)

	// In test mode, skip actual unmount operations
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual unmount for %s", targetPath)
		// Still try to remove the directory in test mode
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			klog.Warningf("Failed to remove target path %s: %v", targetPath, err)
		}
		timer.ObserveSuccess()
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	// Check if mounted
	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if mounted {
		// Unmount
		klog.V(4).Infof("Executing umount command for: %s", targetPath)
		if err := mount.Unmount(ctx, targetPath); err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to unmount: %v", err)
		}
	} else {
		klog.V(4).Infof("Path %s is not mounted, skipping unmount", targetPath)
	}

	// Always attempt to remove the target path (best effort)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("Failed to remove target path %s: %v", targetPath, err)
	}

	klog.V(4).Infof("Unmounted volume %s from %s", volumeID, targetPath)
	timer.ObserveSuccess()
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetVolumeStats returns volume capacity statistics.
func (s *NodeService) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	klog.V(4).Infof("NodeGetVolumeStats called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
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

	// In test mode, skip mount check and return mock stats
	if s.testMode {
		klog.V(4).Infof("Test mode: returning mock stats for %s", volumePath)
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:      csi.VolumeUsage_BYTES,
					Total:     1073741824, // 1GB
					Used:      104857600,  // 100MB
					Available: 968884224,  // ~924MB
				},
			},
		}, nil
	}

	// Check if the path is mounted
	mounted, err := mount.IsMounted(ctx, volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}
	if !mounted {
		return nil, status.Errorf(codes.InvalidArgument, "Volume is not mounted at path %s", volumePath)
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

// NodeExpandVolume expands a volume on the node.
// For NFS volumes, no action is needed as the server handles quota changes.
// For NVMe-oF block volumes, no action is needed.
// For NVMe-oF filesystem volumes, we resize the filesystem.
func (s *NodeService) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	klog.V(4).Infof("NodeExpandVolume called with request: %+v", req)

	// Validate request
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume path is required")
	}

	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()

	// Check if volume path exists
	if _, statErr := os.Stat(volumePath); os.IsNotExist(statErr) {
		return nil, status.Errorf(codes.NotFound, "volume path %s does not exist", volumePath)
	}

	// In test mode, skip mount check and return success immediately
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping volume expansion for %s", volumePath)
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
		}, nil
	}

	// Check if the path is mounted
	mounted, err := mount.IsMounted(ctx, volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}
	if !mounted {
		return nil, status.Errorf(codes.InvalidArgument, "Volume is not mounted at path %s", volumePath)
	}

	// Determine protocol by detecting filesystem type at the mount path
	protocol := s.detectProtocolFromStagingPath(ctx, volumePath)

	klog.V(4).Infof("Expanding volume %s (protocol: %s) at path %s", volumeID, protocol, volumePath)

	// For NFS volumes, no node-side expansion is needed
	if protocol == ProtocolNFS {
		klog.Info("NFS volume expansion handled by controller, no node-side action needed")
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
		}, nil
	}

	// For NVMe-oF volumes, check if this is a block or filesystem volume
	volumeCap := req.GetVolumeCapability()
	if volumeCap != nil && volumeCap.GetBlock() != nil {
		klog.Info("Block volume expansion, no filesystem resize needed")
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
		}, nil
	}

	// For filesystem volumes, we need to resize the filesystem
	klog.V(4).Infof("Resizing filesystem on volume path: %s", volumePath)

	// Detect filesystem type
	fsType, err := detectFilesystemType(ctx, volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to detect filesystem type: %v", err)
	}

	klog.V(4).Infof("Detected filesystem type: %s", fsType)

	// Resize based on filesystem type
	if err := resizeFilesystem(ctx, volumePath, fsType); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to resize filesystem: %v", err)
	}

	klog.V(4).Infof("Resized filesystem for volume %s", volumeID)

	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
	}, nil
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
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
					},
				},
			},
		},
	}, nil
}

// NodeGetInfo returns node information.
func (s *NodeService) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(4).Info("NodeGetInfo called")

	// Register this node with the node registry
	if s.nodeRegistry != nil {
		s.nodeRegistry.Register(s.nodeID)
		klog.V(4).Infof("Registered node %s with node registry", s.nodeID)
	}

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

// detectFilesystemType detects the filesystem type at the given mount point.
// It uses findmnt to determine the filesystem type.
func detectFilesystemType(ctx context.Context, mountPath string) (string, error) {
	// Use findmnt to get filesystem information
	// -n = no headings, -o FSTYPE = only output filesystem type
	cmd := exec.CommandContext(ctx, "findmnt", "-n", "-o", "FSTYPE", mountPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", status.Errorf(codes.Internal, "Failed to detect filesystem type: %v, output: %s", err, string(output))
	}

	fsType := strings.TrimSpace(string(output))
	if fsType == "" {
		return "", status.Error(codes.Internal, "Empty filesystem type returned from findmnt")
	}

	return fsType, nil
}

// resizeFilesystem resizes the filesystem at the given path based on filesystem type.
func resizeFilesystem(ctx context.Context, mountPath, fsType string) error {
	switch fsType {
	case fsTypeExt2, fsTypeExt3, fsTypeExt4:
		// For ext filesystems, we need to find the underlying device
		// Use findmnt to get the source device
		cmd := exec.CommandContext(ctx, "findmnt", "-n", "-o", "SOURCE", mountPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to find device for mount path: %v, output: %s", err, string(output))
		}

		device := strings.TrimSpace(string(output))
		if device == "" {
			return status.Error(codes.Internal, "Empty device path returned from findmnt")
		}

		klog.V(4).Infof("Resizing ext filesystem on device %s", device)
		// #nosec G204 -- device path is validated via findmnt output
		cmd = exec.CommandContext(ctx, "resize2fs", device)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return status.Errorf(codes.Internal, "resize2fs failed: %v, output: %s", err, string(output))
		}
		klog.V(4).Infof("resize2fs output: %s", string(output))
		return nil

	case fsTypeXFS:
		// For XFS, xfs_growfs operates on the mount point
		klog.V(4).Infof("Resizing XFS filesystem at mount point %s", mountPath)
		cmd := exec.CommandContext(ctx, "xfs_growfs", mountPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return status.Errorf(codes.Internal, "xfs_growfs failed: %v, output: %s", err, string(output))
		}
		klog.V(4).Infof("xfs_growfs output: %s", string(output))
		return nil

	default:
		return status.Errorf(codes.Unimplemented, "Filesystem type %s is not supported for expansion", fsType)
	}
}
