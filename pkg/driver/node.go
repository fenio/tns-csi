package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
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

// Static errors for node operations.
var (
	ErrNVMeCLINotFound    = errors.New("nvme command not found - please install nvme-cli")
	ErrNVMeDeviceNotFound = errors.New("NVMe device not found")
	ErrNVMeDeviceTimeout  = errors.New("timeout waiting for NVMe device to appear")
	ErrUnsupportedFSType  = errors.New("unsupported filesystem type")
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
		mounted, err := s.isMounted(ctx, stagingTargetPath)
		if err != nil {
			klog.Warningf("Failed to check if staging path is mounted: %v", err)
		}
		if mounted {
			umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			//nolint:gosec // umount command with path variable is expected for CSI driver
			cmd := exec.CommandContext(umountCtx, "umount", stagingTargetPath)
			if output, err := cmd.CombinedOutput(); err != nil {
				klog.Warningf("Failed to unmount staging path: %v, output: %s", err, string(output))
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

// publishNFSVolume publishes an NFS volume.
func (s *NodeService) publishNFSVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	volumeContext := req.GetVolumeContext()

	// Get server and share from volume context (set during CreateVolume)
	server := volumeContext["server"]
	share := volumeContext["share"]

	if server == "" || share == "" {
		return nil, status.Error(codes.InvalidArgument, "server and share must be provided in volume context for NFS volumes")
	}

	klog.Infof("Mounting NFS volume %s from %s:%s to %s", volumeID, server, share, targetPath)

	// Check if target path exists, create if not
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating target path: %s", targetPath)
		if err := os.MkdirAll(targetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create target path: %v", err)
		}
	}

	// Check if already mounted
	mounted, err := s.isMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Mount NFS share
	nfsSource := fmt.Sprintf("%s:%s", server, share)
	mountOptions := []string{"vers=4.2", "nolock"}

	// Add read-only flag if requested
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	// Construct mount command
	args := []string{"-t", "nfs", "-o", joinMountOptions(mountOptions), nfsSource, targetPath}

	klog.V(4).Infof("Executing mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount NFS share: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully mounted NFS volume %s at %s", volumeID, targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

// publishBlockVolume publishes a block volume by bind mounting the device file from staging to target.
func (s *NodeService) publishBlockVolume(ctx context.Context, stagingTargetPath, targetPath string, readonly bool) (*csi.NodePublishVolumeResponse, error) {
	klog.Infof("Publishing block device from %s to %s", stagingTargetPath, targetPath)

	// Verify staging path exists and is a device or symlink
	stagingInfo, err := os.Stat(stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Staging path %s not found: %v", stagingTargetPath, err)
	}

	// For block volumes, staging path should be a file (device node or symlink), not a directory
	if stagingInfo.IsDir() {
		return nil, status.Errorf(codes.Internal, "Staging path %s is a directory, expected block device or symlink", stagingTargetPath)
	}

	// For block volumes, CSI driver must create the parent directory and target file.
	// Create parent directory if it doesn't exist
	targetDir := filepath.Dir(targetPath)
	if mkdirErr := os.MkdirAll(targetDir, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create target directory %s: %v", targetDir, mkdirErr)
	}

	// Create target file if it doesn't exist
	if _, statErr := os.Stat(targetPath); os.IsNotExist(statErr) {
		klog.V(4).Infof("Creating target file: %s", targetPath)
		//nolint:gosec // Target path from CSI request is validated by Kubernetes CSI framework
		file, fileErr := os.OpenFile(targetPath, os.O_CREATE, 0o600)
		if fileErr != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create target file %s: %v", targetPath, fileErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			klog.Warningf("Failed to close target file %s: %v", targetPath, closeErr)
		}
	}

	// Check if already mounted
	mounted, err := s.isDeviceMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if device is mounted: %v", err)
	}
	if mounted {
		klog.V(4).Infof("Target path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Bind mount the device from staging to target
	mountOptions := []string{"bind"}
	if readonly {
		mountOptions = append(mountOptions, "ro")
	}

	args := []string{"-o", joinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Cleanup: remove target file on failure
		if removeErr := os.Remove(targetPath); removeErr != nil && !os.IsNotExist(removeErr) {
			klog.Warningf("Failed to remove target file %s during cleanup: %v", targetPath, removeErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to bind mount block device: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully bind mounted block device to %s", targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
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
	mounted, err := s.isMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if !mounted {
		klog.V(4).Infof("Path %s is not mounted, nothing to do", targetPath)
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	// Unmount
	klog.V(4).Infof("Executing umount command for: %s", targetPath)
	umountCtx, umountCancel := context.WithTimeout(ctx, 30*time.Second)
	defer umountCancel()
	//nolint:gosec // umount command with path variable is expected for CSI driver
	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount: %v, output: %s", err, string(output))
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

// isMounted checks if a path is mounted.
func (s *NodeService) isMounted(ctx context.Context, targetPath string) (bool, error) {
	// Use findmnt to check if path is mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "TARGET", "-n", "-l", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero exit code if path is not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// joinMountOptions joins mount options with commas.
func joinMountOptions(options []string) string {
	if len(options) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(options[0])
	for i := 1; i < len(options); i++ {
		builder.WriteString(",")
		builder.WriteString(options[i])
	}
	return builder.String()
}

// safeUint64ToInt64 safely converts uint64 to int64, capping at math.MaxInt64.
// This is necessary for CSI VolumeUsage which uses int64 per the protobuf spec.
func safeUint64ToInt64(val uint64) int64 {
	const maxInt64 = 9223372036854775807 // math.MaxInt64
	if val > maxInt64 {
		return maxInt64
	}
	return int64(val)
}

// Protocol-specific staging functions

// stageNVMeOFVolume stages an NVMe-oF volume by connecting to the target.
func (s *NodeService) stageNVMeOFVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Get NVMe-oF parameters from volume context
	nqn := volumeContext["nqn"]
	server := volumeContext["server"]
	transport := volumeContext["transport"]
	port := volumeContext["port"]

	if nqn == "" || server == "" {
		return nil, status.Error(codes.InvalidArgument, "nqn and server must be provided in volume context for NVMe-oF volumes")
	}

	// Default values
	if transport == "" {
		transport = "tcp"
	}
	if port == "" {
		port = "4420"
	}

	// Check if this is a block volume or filesystem volume
	isBlockVolume := volumeCapability.GetBlock() != nil

	klog.Infof("Staging NVMe-oF volume %s (block mode: %v): connecting to %s:%s (NQN: %s)", volumeID, isBlockVolume, server, port, nqn)

	// Check if already connected
	devicePath, err := s.findNVMeDeviceByNQN(ctx, nqn)
	if err == nil && devicePath != "" {
		klog.Infof("NVMe-oF device already connected at %s", devicePath)
		// Device already connected, proceed based on volume type
		if isBlockVolume {
			return s.stageBlockDevice(devicePath, stagingTargetPath)
		}
		return s.formatAndMountNVMeDevice(ctx, devicePath, stagingTargetPath, volumeCapability)
	}

	// Check if nvme-cli is installed
	if checkErr := s.checkNVMeCLI(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "nvme-cli not available: %v", checkErr)
	}

	// Discover the NVMe-oF target
	klog.V(4).Infof("Discovering NVMe-oF target at %s:%s", server, port)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
	defer discoverCancel()
	//nolint:gosec // nvme discover with volume context variables is expected for CSI driver
	discoverCmd := exec.CommandContext(discoverCtx, "nvme", "discover", "-t", transport, "-a", server, "-s", port)
	if output, discoverErr := discoverCmd.CombinedOutput(); discoverErr != nil {
		klog.Warningf("NVMe discover failed (this may be OK if target is already known): %v, output: %s", discoverErr, string(output))
	}

	// Connect to the NVMe-oF target
	klog.Infof("Connecting to NVMe-oF target: %s", nqn)
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connectCancel()
	//nolint:gosec // nvme connect with volume context variables is expected for CSI driver
	connectCmd := exec.CommandContext(connectCtx, "nvme", "connect", "-t", transport, "-n", nqn, "-a", server, "-s", port)
	output, err := connectCmd.CombinedOutput()
	if err != nil {
		// Check if already connected
		if strings.Contains(string(output), "already connected") {
			klog.V(4).Infof("NVMe device already connected (output: %s)", string(output))
		} else {
			return nil, status.Errorf(codes.Internal, "Failed to connect to NVMe-oF target: %v, output: %s", err, string(output))
		}
	}

	// Wait for device to appear and find the device path
	devicePath, err = s.waitForNVMeDevice(ctx, nqn, 30*time.Second)
	if err != nil {
		// Cleanup: disconnect on failure
		if disconnectErr := s.disconnectNVMeOF(ctx, nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect NVMe-oF after device wait failure: %v", disconnectErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after connection: %v", err)
	}

	klog.Infof("NVMe-oF device connected at %s", devicePath)

	// Stage based on volume type
	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountNVMeDevice(ctx, devicePath, stagingTargetPath, volumeCapability)
}

// unstageNVMeOFVolume unstages an NVMe-oF volume by disconnecting from the target.
func (s *NodeService) unstageNVMeOFVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.Infof("Unstaging NVMe-oF volume %s from %s", volumeID, stagingTargetPath)

	// Get NQN from volume context
	nqn := volumeContext["nqn"]
	if nqn == "" {
		// Try to decode from volumeID
		meta, err := decodeVolumeID(volumeID)
		if err != nil {
			klog.Warningf("Failed to get NQN for volume %s: %v", volumeID, err)
			return &csi.NodeUnstageVolumeResponse{}, nil
		}
		nqn = meta.NVMeOFNQN
	}

	// Check if mounted and unmount if necessary
	mounted, err := s.isMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.Infof("Unmounting staging path: %s", stagingTargetPath)
		umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		//nolint:gosec // umount command with path variable is expected for CSI driver
		cmd := exec.CommandContext(umountCtx, "umount", stagingTargetPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v, output: %s", err, string(output))
		}
	}

	// Disconnect from NVMe-oF target
	if nqn != "" {
		if err := s.disconnectNVMeOF(ctx, nqn); err != nil {
			klog.Warningf("Failed to disconnect NVMe-oF device (continuing anyway): %v", err)
		} else {
			klog.Infof("Successfully disconnected from NVMe-oF target: %s", nqn)
		}
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NVMe-oF helper functions

// checkNVMeCLI checks if nvme-cli is installed.
func (s *NodeService) checkNVMeCLI(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "nvme", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrNVMeCLINotFound, err)
	}
	return nil
}

// findNVMeDeviceByNQN finds the device path for a given NQN.
//
//nolint:gocognit // Complex NVMe device discovery - refactoring would risk stability of working code
func (s *NodeService) findNVMeDeviceByNQN(ctx context.Context, nqn string) (string, error) {
	// Use nvme list-subsys which shows NQN
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	subsysOutput, err := subsysCmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v", err)
		// Fall back to checking /sys/class/nvme
		return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
	}

	// Parse output to find NQN and extract controller name
	// The JSON format from nvme list-subsys has: "Name" : "nvmeX" under Paths
	// We need to construct the device path as /dev/nvmeXn1
	lines := strings.Split(string(subsysOutput), "\n")
	foundNQN := false
	for i, line := range lines {
		// Look for the NQN line
		if strings.Contains(line, nqn) {
			foundNQN = true
			// Now look ahead for the "Name" field in the Paths section
			for j := i; j < len(lines) && j < i+20; j++ {
				if strings.Contains(lines[j], "\"Name\"") && strings.Contains(lines[j], "nvme") {
					// Extract controller name - format: "Name" : "nvme0"
					parts := strings.Split(lines[j], "\"")
					for k := 0; k < len(parts)-1; k++ {
						if parts[k] == "Name" && k+2 < len(parts) {
							controllerName := strings.TrimSpace(parts[k+2])
							// Construct device path - typically nvme0 -> /dev/nvme0n1
							devicePath := fmt.Sprintf("/dev/%sn1", controllerName)
							return devicePath, nil
						}
					}
				}
			}
		}
	}

	if foundNQN {
		klog.Warningf("Found NQN but could not extract device name, falling back to sysfs")
	}

	// Fall back to checking /sys/class/nvme if JSON parsing failed
	return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
}

// findNVMeDeviceByNQNFromSys finds NVMe device by checking /sys/class/nvme.
func (s *NodeService) findNVMeDeviceByNQNFromSys(ctx context.Context, nqn string) (string, error) {
	// Read /sys/class/nvme/nvmeX/subsysnqn for each device
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", nvmeDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		deviceName := entry.Name()
		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")

		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			continue
		}

		if strings.TrimSpace(string(data)) == nqn {
			// Found the device, now find the namespace
			// Typically nvme0 -> /dev/nvme0n1
			devicePath := fmt.Sprintf("/dev/%sn1", deviceName)
			if _, err := os.Stat(devicePath); err == nil {
				return devicePath, nil
			}
		}
	}

	return "", fmt.Errorf("%w for NQN: %s", ErrNVMeDeviceNotFound, nqn)
}

// waitForNVMeDevice waits for the NVMe device to appear after connection.
func (s *NodeService) waitForNVMeDevice(ctx context.Context, nqn string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		devicePath, err := s.findNVMeDeviceByNQN(ctx, nqn)
		if err == nil && devicePath != "" {
			// Verify device is accessible
			if _, err := os.Stat(devicePath); err == nil {
				klog.Infof("NVMe device found at %s after %d attempts", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("%w after %d attempts", ErrNVMeDeviceTimeout, attempt)
}

// disconnectNVMeOF disconnects from an NVMe-oF target.
func (s *NodeService) disconnectNVMeOF(ctx context.Context, nqn string) error {
	klog.V(4).Infof("Disconnecting from NVMe-oF target: %s", nqn)

	disconnectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(disconnectCtx, "nvme", "disconnect", "-n", nqn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already disconnected
		if strings.Contains(string(output), "No subsystems") || strings.Contains(string(output), "not found") {
			klog.V(4).Infof("NVMe device already disconnected")
			return nil
		}
		return fmt.Errorf("failed to disconnect NVMe-oF device: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully disconnected from NVMe-oF target")
	return nil
}

// formatAndMountNVMeDevice formats (if needed) and mounts an NVMe device.
func (s *NodeService) formatAndMountNVMeDevice(ctx context.Context, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability) (*csi.NodeStageVolumeResponse, error) {
	klog.Infof("Formatting and mounting NVMe device %s to %s", devicePath, stagingTargetPath)

	// Determine filesystem type from volume capability
	fsType := "ext4" // default
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Check if device is already formatted
	needsFormat, err := s.needsFormat(ctx, devicePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if needsFormat {
		klog.Infof("Formatting device %s with filesystem %s", devicePath, fsType)
		if formatErr := s.formatDevice(ctx, devicePath, fsType); formatErr != nil {
			return nil, status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}
	} else {
		klog.V(4).Infof("Device %s is already formatted", devicePath)
	}

	// Create staging target path if it doesn't exist
	if mkdirErr := os.MkdirAll(stagingTargetPath, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", mkdirErr)
	}

	// Check if already mounted
	mounted, err := s.isMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Mount the device
	klog.Infof("Mounting device %s to %s", devicePath, stagingTargetPath)
	mountOptions := []string{}
	if mnt := volumeCapability.GetMount(); mnt != nil {
		mountOptions = mnt.MountFlags
	}

	args := []string{devicePath, stagingTargetPath}
	if len(mountOptions) > 0 {
		args = []string{"-o", joinMountOptions(mountOptions), devicePath, stagingTargetPath}
	}

	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully mounted NVMe device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

// needsFormat checks if a device needs to be formatted.
func (s *NodeService) needsFormat(ctx context.Context, devicePath string) (bool, error) {
	// Use blkid to check if device has a filesystem
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "blkid", devicePath)
	output, err := cmd.CombinedOutput()

	// If blkid returns non-zero and output is empty, device needs formatting
	if err != nil {
		if len(output) == 0 {
			return true, nil
		}
		// Check if error is because no filesystem found
		if strings.Contains(string(output), "does not contain") {
			return true, nil
		}
	}

	// Device has a filesystem
	return false, nil
}

// formatDevice formats a device with the specified filesystem.
func (s *NodeService) formatDevice(ctx context.Context, devicePath, fsType string) error {
	// Formatting can take time, allow up to 60 seconds
	formatCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var cmd *exec.Cmd

	switch fsType {
	case "ext4":
		// -F force, don't ask for confirmation
		cmd = exec.CommandContext(formatCtx, "mkfs.ext4", "-F", devicePath)
	case "ext3":
		cmd = exec.CommandContext(formatCtx, "mkfs.ext3", "-F", devicePath)
	case "xfs":
		// -f force overwrite
		cmd = exec.CommandContext(formatCtx, "mkfs.xfs", "-f", devicePath)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedFSType, fsType)
	}

	klog.V(4).Infof("Running format command: %v", cmd.Args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("format command failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Format output: %s", string(output))
	return nil
}

// stageBlockDevice stages a raw block device by creating a symlink at staging path.
func (s *NodeService) stageBlockDevice(devicePath, stagingTargetPath string) (*csi.NodeStageVolumeResponse, error) {
	klog.Infof("Staging block device %s to %s", devicePath, stagingTargetPath)

	// Verify device exists
	if _, err := os.Stat(devicePath); err != nil {
		return nil, status.Errorf(codes.Internal, "Device path %s not found: %v", devicePath, err)
	}

	// Check if staging path already exists
	if _, err := os.Stat(stagingTargetPath); err == nil {
		// Staging path exists - check if it's a valid symlink or device
		klog.V(4).Infof("Staging path %s already exists", stagingTargetPath)
		// Verify it points to the correct device
		targetDevice, err := filepath.EvalSymlinks(stagingTargetPath)
		if err == nil && targetDevice == devicePath {
			klog.V(4).Infof("Staging path already points to correct device")
			return &csi.NodeStageVolumeResponse{}, nil
		}
		// Remove existing staging path if it doesn't match
		klog.Warningf("Removing incorrect staging path: %s", stagingTargetPath)
		if err := os.Remove(stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to remove incorrect staging path: %v", err)
		}
	}

	// Create parent directory if needed
	stagingDir := filepath.Dir(stagingTargetPath)
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging directory: %v", err)
	}

	// Create symlink from staging path to device
	if err := os.Symlink(devicePath, stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create symlink from %s to %s: %v", stagingTargetPath, devicePath, err)
	}

	klog.Infof("Successfully staged block device at %s -> %s", stagingTargetPath, devicePath)
	return &csi.NodeStageVolumeResponse{}, nil
}

// isDeviceMounted checks if a device path is mounted (for block devices).
func (s *NodeService) isDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	// For block devices, check if it's bind mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "SOURCE", "-n", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero if not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}
