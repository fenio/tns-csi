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

	klog.V(4).Infof("Mounting NFS volume %s from %s:%s to %s", volumeID, server, share, targetPath)

	// Check if target path exists, create if not
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating target path: %s", targetPath)
		if err := os.MkdirAll(targetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create target path: %v", err)
		}
	}

	// In test mode, skip actual mount operations
	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual mount for %s", targetPath)
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

	// Mount NFS share
	nfsSource := fmt.Sprintf("%s:%s", server, share)
	mountOptions := getNFSMountOptions()

	// Add read-only flag if requested
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	// Construct mount command
	args := []string{"-t", "nfs", "-o", mount.JoinMountOptions(mountOptions), nfsSource, targetPath}

	klog.V(4).Infof("Executing mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount NFS share: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Mounted NFS volume %s at %s", volumeID, targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}
