package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// stageNFSVolume stages an NFS volume by mounting it to the staging target path.
// This allows multiple pods on the same node to share a single NFS mount.
func (s *NodeService) stageNFSVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	// Get server and share from volume context (set during CreateVolume)
	server := volumeContext["server"]
	share := volumeContext["share"]

	if server == "" || share == "" {
		return nil, status.Error(codes.InvalidArgument, "server and share must be provided in volume context for NFS volumes")
	}

	klog.V(4).Infof("Staging NFS volume %s from %s:%s to %s", volumeID, server, share, stagingTargetPath)

	// Check if staging target path exists, create if not
	if _, err := os.Stat(stagingTargetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating staging target path: %s", stagingTargetPath)
		if err := os.MkdirAll(stagingTargetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
		}
	}

	// In test mode, skip actual mount operations
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual NFS mount for staging %s", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Check if already mounted
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Mount NFS share to staging path
	nfsSource := fmt.Sprintf("%s:%s", server, share)

	// Get user-specified mount options from StorageClass (passed via VolumeCapability)
	var userMountOptions []string
	if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getNFSMountOptions(userMountOptions)

	klog.V(4).Infof("NFS mount options: user=%v, final=%v", userMountOptions, mountOptions)

	// Construct mount command
	args := []string{"-t", "nfs", "-o", mount.JoinMountOptions(mountOptions), nfsSource, stagingTargetPath}

	klog.V(4).Infof("Executing mount command for staging: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount NFS share for staging: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Staged NFS volume %s at %s", volumeID, stagingTargetPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

// unstageNFSVolume unstages an NFS volume by unmounting it from the staging target path.
func (s *NodeService) unstageNFSVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging NFS volume %s from %s", volumeID, stagingTargetPath)

	// In test mode, skip actual unmount operations
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual unmount for NFS staging %s", stagingTargetPath)
		// Still try to remove the directory in test mode
		if err := os.Remove(stagingTargetPath); err != nil && !os.IsNotExist(err) {
			klog.Warningf("Failed to remove staging target path %s: %v", stagingTargetPath, err)
		}
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// Check if mounted
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting NFS staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount NFS staging path: %v", err)
		}
	} else {
		klog.V(4).Infof("Staging path %s is not mounted, skipping unmount", stagingTargetPath)
	}

	// Remove the staging directory (best effort)
	if err := os.Remove(stagingTargetPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("Failed to remove staging target path %s: %v", stagingTargetPath, err)
	}

	klog.V(4).Infof("Unstaged NFS volume %s from %s", volumeID, stagingTargetPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// publishNFSVolume publishes an NFS volume by bind-mounting from staging path to target path.
func (s *NodeService) publishNFSVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Publishing NFS volume %s from staging %s to %s", volumeID, stagingTargetPath, targetPath)

	// Check if target path exists, create if not
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating target path: %s", targetPath)
		if err := os.MkdirAll(targetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create target path: %v", err)
		}
	}

	// In test mode, skip actual mount operations
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual bind mount for %s", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Check if already mounted
	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Build mount options for bind mount
	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	// Bind mount from staging path to target path
	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to bind mount NFS volume: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Published NFS volume %s at %s", volumeID, targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}
