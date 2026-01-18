package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for iSCSI operations.
var (
	ErrISCSIAdmNotFound    = errors.New("iscsiadm command not found - please install open-iscsi")
	ErrISCSIDeviceNotFound = errors.New("iSCSI device not found")
	ErrISCSIDeviceTimeout  = errors.New("timeout waiting for iSCSI device to appear")
	ErrISCSILoginFailed    = errors.New("failed to login to iSCSI target")
)

// defaultISCSIMountOptions are sensible defaults for iSCSI filesystem mounts.
var defaultISCSIMountOptions = []string{"noatime", "_netdev"}

// iscsiConnectionParams holds validated iSCSI connection parameters.
type iscsiConnectionParams struct {
	iqn    string
	server string
	port   string
	lun    int
}

// stageISCSIVolume stages an iSCSI volume by logging into the target.
func (s *NodeService) stageISCSIVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Validate and extract connection parameters
	params, err := s.validateISCSIParams(volumeContext)
	if err != nil {
		return nil, err
	}

	isBlockVolume := volumeCapability.GetBlock() != nil
	datasetName := volumeContext["datasetName"]
	klog.V(4).Infof("Staging iSCSI volume %s (block mode: %v): server=%s:%s, IQN=%s, LUN=%d, dataset=%s",
		volumeID, isBlockVolume, params.server, params.port, params.iqn, params.lun, datasetName)

	// Try to reuse existing connection (idempotency)
	if devicePath, findErr := s.findISCSIDevice(ctx, params); findErr == nil && devicePath != "" {
		klog.V(4).Infof("iSCSI device already connected at %s - reusing existing connection", devicePath)
		return s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	}

	// Check if iscsiadm is installed
	if checkErr := s.checkISCSIAdm(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "open-iscsi not available: %v", checkErr)
	}

	// Discover and login to iSCSI target
	if loginErr := s.loginISCSITarget(ctx, params); loginErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to login to iSCSI target: %v", loginErr)
	}

	// Wait for device to appear
	devicePath, err := s.waitForISCSIDevice(ctx, params, 30*time.Second)
	if err != nil {
		// Cleanup: logout on failure
		if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
			klog.Warningf("Failed to logout from iSCSI target after device wait failure: %v", logoutErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to find iSCSI device after login: %v", err)
	}

	klog.V(4).Infof("iSCSI device connected at %s (IQN: %s, LUN: %d, dataset: %s)",
		devicePath, params.iqn, params.lun, datasetName)

	return s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
}

// validateISCSIParams validates and extracts iSCSI connection parameters from volume context.
func (s *NodeService) validateISCSIParams(volumeContext map[string]string) (*iscsiConnectionParams, error) {
	params := &iscsiConnectionParams{
		iqn:    volumeContext[VolumeContextKeyISCSIIQN],
		server: volumeContext["server"],
		port:   volumeContext["port"],
		lun:    0, // Always LUN 0 with dedicated targets
	}

	if params.iqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "iSCSI IQN and server must be provided in volume context")
	}

	// Default port
	if params.port == "" {
		params.port = "3260"
	}

	return params, nil
}

// checkISCSIAdm checks if iscsiadm is installed.
func (s *NodeService) checkISCSIAdm(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "iscsiadm", "--version")
	if err := cmd.Run(); err != nil {
		return ErrISCSIAdmNotFound
	}
	return nil
}

// loginISCSITarget discovers and logs into an iSCSI target.
func (s *NodeService) loginISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	portal := params.server + ":" + params.port

	// Step 1: Discovery
	klog.V(4).Infof("Discovering iSCSI targets at %s", portal)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 30*time.Second)
	defer discoverCancel()

	//nolint:gosec // iscsiadm with portal from volume context is expected for CSI driver
	discoverCmd := exec.CommandContext(discoverCtx, "iscsiadm", "-m", "discovery", "-t", "sendtargets", "-p", portal)
	output, err := discoverCmd.CombinedOutput()
	if err != nil {
		// Discovery failure is not fatal if target is already known
		klog.Warningf("iSCSI discovery failed (may be OK if target is known): %v, output: %s", err, string(output))
	} else {
		klog.V(4).Infof("iSCSI discovery output: %s", string(output))
	}

	// Step 2: Login
	klog.V(4).Infof("Logging into iSCSI target: %s at %s", params.iqn, portal)
	loginCtx, loginCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loginCancel()

	//nolint:gosec // iscsiadm login with IQN and portal from volume context is expected for CSI driver
	loginCmd := exec.CommandContext(loginCtx, "iscsiadm", "-m", "node", "-T", params.iqn, "-p", portal, "--login")
	output, err = loginCmd.CombinedOutput()
	if err != nil {
		// Check if already logged in
		alreadyLoggedIn := strings.Contains(string(output), "already present") ||
			strings.Contains(string(output), "session already exists")
		if alreadyLoggedIn {
			klog.V(4).Infof("iSCSI target already logged in: %s", params.iqn)
			return nil
		}
		klog.Errorf("iSCSI login failed for target %s at %s: %v, output: %s", params.iqn, portal, err, string(output))
		return fmt.Errorf("%w: %s", ErrISCSILoginFailed, string(output))
	}

	klog.V(4).Infof("Successfully logged into iSCSI target: %s", params.iqn)
	return nil
}

// logoutISCSITarget logs out from an iSCSI target.
func (s *NodeService) logoutISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	portal := params.server + ":" + params.port

	klog.V(4).Infof("Logging out from iSCSI target: %s at %s", params.iqn, portal)
	logoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	//nolint:gosec // iscsiadm logout with IQN and portal from volume context is expected for CSI driver
	cmd := exec.CommandContext(logoutCtx, "iscsiadm", "-m", "node", "-T", params.iqn, "-p", portal, "--logout")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already logged out
		alreadyLoggedOut := strings.Contains(string(output), "No matching sessions") ||
			strings.Contains(string(output), "not found")
		if alreadyLoggedOut {
			klog.V(4).Infof("iSCSI target already logged out")
			return nil
		}
		return err
	}

	klog.V(4).Infof("Successfully logged out from iSCSI target: %s", params.iqn)
	return nil
}

// findISCSIDevice finds the device path for an iSCSI LUN.
func (s *NodeService) findISCSIDevice(ctx context.Context, params *iscsiConnectionParams) (string, error) {
	_ = ctx // Reserved for future use

	// Look for device in /dev/disk/by-path/
	// Format: ip-<server>:<port>-iscsi-<iqn>-lun-<lun>
	pattern := "ip-" + params.server + ":" + params.port + "-iscsi-" + params.iqn + "-lun-*"
	byPathDir := "/dev/disk/by-path"

	matches, err := filepath.Glob(filepath.Join(byPathDir, pattern))
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", ErrISCSIDeviceNotFound
	}

	// Resolve the symlink to get the actual device path
	devicePath, err := filepath.EvalSymlinks(matches[0])
	if err != nil {
		return "", err
	}

	klog.V(4).Infof("Found iSCSI device: %s -> %s", matches[0], devicePath)
	return devicePath, nil
}

// waitForISCSIDevice waits for the iSCSI device to appear after login.
func (s *NodeService) waitForISCSIDevice(ctx context.Context, params *iscsiConnectionParams, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		devicePath, err := s.findISCSIDevice(ctx, params)
		if err == nil && devicePath != "" {
			// Verify device is accessible
			if _, statErr := os.Stat(devicePath); statErr == nil {
				klog.V(4).Infof("iSCSI device found at %s after %d attempts", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return "", ErrISCSIDeviceTimeout
}

// stageISCSIDevice stages an iSCSI device as either block or filesystem volume.
func (s *NodeService) stageISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// For filesystem volumes, wait for device to be fully initialized
	if !isBlockVolume {
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			return nil, status.Errorf(codes.Internal, "Device initialization timeout: %v", err)
		}

		// Force device rescan
		if err := forceDeviceRescan(ctx, devicePath); err != nil {
			klog.Warningf("Device rescan warning for %s: %v (continuing anyway)", devicePath, err)
		}

		// Stabilization delay
		const deviceMetadataDelay = 2 * time.Second
		klog.V(4).Infof("Waiting %v for device %s metadata to stabilize", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)
	}

	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, volumeContext)
}

// formatAndMountISCSIDevice formats (if needed) and mounts an iSCSI device.
func (s *NodeService) formatAndMountISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	iqn := volumeContext[VolumeContextKeyISCSIIQN]
	klog.V(4).Infof("Formatting and mounting iSCSI device: device=%s, path=%s, volume=%s, dataset=%s, IQN=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, iqn)

	// Log device information
	s.logDeviceInfo(ctx, devicePath)

	// Verify device size
	if err := s.verifyDeviceSize(ctx, devicePath, volumeContext); err != nil {
		klog.Errorf("Device size verification FAILED for %s: %v", devicePath, err)
		return nil, status.Errorf(codes.FailedPrecondition,
			"Device size mismatch detected - refusing to mount: %v", err)
	}

	// Determine filesystem type
	fsType := "ext4"
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Check if device is cloned from snapshot
	isClone := false
	if cloned, exists := volumeContext[VolumeContextKeyClonedFromSnap]; exists && cloned == VolumeContextValueTrue {
		isClone = true
		klog.V(4).Infof("Volume %s was cloned from snapshot - adding stabilization delay", volumeID)
		const cloneStabilizationDelay = 5 * time.Second
		time.Sleep(cloneStabilizationDelay)
	}

	// Handle formatting
	if err := s.handleDeviceFormatting(ctx, volumeID, devicePath, fsType, datasetName, iqn, isClone); err != nil {
		return nil, err
	}

	// Create staging target path
	if err := os.MkdirAll(stagingTargetPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
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

	// Mount the device
	klog.V(4).Infof("Mounting device %s to %s", devicePath, stagingTargetPath)

	var userMountOptions []string
	if mnt := volumeCapability.GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getISCSIMountOptions(userMountOptions)

	klog.V(4).Infof("iSCSI mount options: user=%v, final=%v", userMountOptions, mountOptions)

	args := []string{devicePath, stagingTargetPath}
	if len(mountOptions) > 0 {
		args = []string{"-o", mount.JoinMountOptions(mountOptions), devicePath, stagingTargetPath}
	}

	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Mounted iSCSI device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

// unstageISCSIVolume unstages an iSCSI volume by logging out from the target.
func (s *NodeService) unstageISCSIVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging iSCSI volume %s from %s", volumeID, stagingTargetPath)

	// Get IQN from volume context
	iqn := volumeContext[VolumeContextKeyISCSIIQN]

	// Check if mounted and unmount if necessary
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
		}
	}

	// If we don't have IQN, we can't logout
	if iqn == "" {
		klog.Warningf("Cannot determine IQN for volume %s - skipping iSCSI logout", volumeID)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// Logout from the iSCSI target
	server := volumeContext["server"]
	port := volumeContext["port"]
	if port == "" {
		port = "3260"
	}

	params := &iscsiConnectionParams{
		iqn:    iqn,
		server: server,
		port:   port,
	}

	klog.V(4).Infof("Logging out from iSCSI target for volume %s: IQN=%s", volumeID, iqn)
	if err := s.logoutISCSITarget(ctx, params); err != nil {
		klog.Warningf("Failed to logout from iSCSI target (continuing anyway): %v", err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// getISCSIMountOptions merges user-provided mount options with sensible defaults.
func getISCSIMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultISCSIMountOptions
	}

	// Build a map of user-specified option keys
	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractISCSIOptionKey(opt)
		userOptionKeys[key] = true
	}

	// Start with user options, then add defaults that don't conflict
	result := make([]string, 0, len(userOptions)+len(defaultISCSIMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultISCSIMountOptions {
		key := extractISCSIOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}

// extractISCSIOptionKey extracts the key from a mount option.
func extractISCSIOptionKey(option string) string {
	for i, c := range option {
		if c == '=' {
			return option[:i]
		}
	}
	return option
}
