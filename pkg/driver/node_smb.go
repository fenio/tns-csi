//nolint:dupl // SMB node operations follow the same pattern as NFS but use CIFS mount type
package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

var errSMBUsernameRequired = errors.New("SMB secret must contain a 'username' key")

// writeSMBCredentialsFile writes SMB credentials to a temporary file with 0600 permissions.
// The caller is responsible for removing the file (defer os.Remove(path)).
// Secret keys: "username" (required), "password" (optional), "domain" (optional).
func writeSMBCredentialsFile(secrets map[string]string) (path string, retErr error) {
	username := secrets["username"]
	if username == "" {
		return "", errSMBUsernameRequired
	}

	f, err := os.CreateTemp("", "smb-credentials-*")
	if err != nil {
		return "", fmt.Errorf("failed to create credentials temp file: %w", err)
	}
	path = f.Name()

	// Clean up on any error after file creation.
	defer func() {
		if retErr != nil {
			f.Close()       //nolint:errcheck,gosec // best-effort cleanup on error path
			os.Remove(path) //nolint:errcheck,gosec // best-effort cleanup on error path
		}
	}()

	if err := f.Chmod(0o600); err != nil {
		return "", fmt.Errorf("failed to set credentials file permissions: %w", err)
	}

	if _, err := fmt.Fprintf(f, "username=%s\n", username); err != nil {
		return "", fmt.Errorf("failed to write credentials: %w", err)
	}
	if password, ok := secrets["password"]; ok {
		if _, err := fmt.Fprintf(f, "password=%s\n", password); err != nil {
			return "", fmt.Errorf("failed to write credentials: %w", err)
		}
	}
	if domain, ok := secrets["domain"]; ok && domain != "" {
		if _, err := fmt.Fprintf(f, "domain=%s\n", domain); err != nil {
			return "", fmt.Errorf("failed to write credentials: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("failed to close credentials file: %w", err)
	}

	return path, nil
}

// isSMBKerberosAuth checks if mount options indicate Kerberos authentication.
func isSMBKerberosAuth(mountOptions []string) bool {
	for _, opt := range mountOptions {
		if strings.HasPrefix(opt, "sec=krb5") {
			return true
		}
	}
	return false
}

// stageSMBVolume stages an SMB volume by mounting it to the staging target path.
func (s *NodeService) stageSMBVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	server := volumeContext["server"]
	share := volumeContext["share"]

	if server == "" || share == "" {
		return nil, status.Error(codes.InvalidArgument, "server and share must be provided in volume context for SMB volumes")
	}

	klog.V(4).Infof("Staging SMB volume %s from //%s/%s to %s", volumeID, server, share, stagingTargetPath)

	if _, err := os.Stat(stagingTargetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating staging target path: %s", stagingTargetPath)
		if err := os.MkdirAll(stagingTargetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
		}
	}

	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual SMB mount for staging %s", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// SMB/CIFS source format: //server/sharename
	cifsSource := fmt.Sprintf("//%s/%s", server, share)

	var userMountOptions []string
	if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getSMBMountOptions(userMountOptions)

	// Handle SMB credentials from nodeStageSecretRef
	secrets := req.GetSecrets()
	if username := secrets["username"]; username != "" && !isSMBKerberosAuth(mountOptions) {
		credFile, credErr := writeSMBCredentialsFile(secrets)
		if credErr != nil {
			return nil, status.Errorf(codes.Internal, "Failed to write SMB credentials file: %v", credErr)
		}
		defer os.Remove(credFile) //nolint:errcheck // best-effort cleanup after mount
		mountOptions = append(mountOptions, "credentials="+credFile)
		klog.V(4).Infof("Using SMB credentials file for volume %s", volumeID)
	} else if !isSMBKerberosAuth(mountOptions) {
		mountOptions = append(mountOptions, "guest")
		klog.V(4).Infof("No SMB credentials provided, using guest access for volume %s", volumeID)
	} else {
		klog.V(4).Infof("Kerberos authentication detected for volume %s, skipping credentials", volumeID)
	}

	klog.V(4).Infof("SMB mount options: user=%v, final=%v", userMountOptions, mountOptions)

	args := []string{"-t", "cifs", "-o", mount.JoinMountOptions(mountOptions), cifsSource, stagingTargetPath}

	klog.V(4).Infof("Executing mount command for staging: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount SMB share for staging: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Staged SMB volume %s at %s", volumeID, stagingTargetPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

// unstageSMBVolume unstages an SMB volume by unmounting it from the staging target path.
func (s *NodeService) unstageSMBVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging SMB volume %s from %s", volumeID, stagingTargetPath)

	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual unmount for SMB staging %s", stagingTargetPath)
		if err := os.Remove(stagingTargetPath); err != nil && !os.IsNotExist(err) {
			klog.Warningf("Failed to remove staging target path %s: %v", stagingTargetPath, err)
		}
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting SMB staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount SMB staging path: %v", err)
		}
	} else {
		klog.V(4).Infof("Staging path %s is not mounted, skipping unmount", stagingTargetPath)
	}

	if err := os.RemoveAll(stagingTargetPath); err != nil {
		klog.Warningf("Failed to remove staging target path %s: %v", stagingTargetPath, err)
	}

	klog.V(4).Infof("Unstaged SMB volume %s from %s", volumeID, stagingTargetPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// publishSMBVolume publishes an SMB volume by bind-mounting from staging path to target path.
func (s *NodeService) publishSMBVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Publishing SMB volume %s from staging %s to %s", volumeID, stagingTargetPath, targetPath)

	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		klog.V(4).Infof("Creating target path: %s", targetPath)
		if err := os.MkdirAll(targetPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create target path: %v", err)
		}
	}

	if s.testMode {
		klog.V(4).Infof("Test mode: skipping actual bind mount for %s", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to bind mount SMB volume: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Published SMB volume %s at %s", volumeID, targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}
