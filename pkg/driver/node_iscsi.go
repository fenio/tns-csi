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

// Static errors for iSCSI operations.
var (
	ErrISCSIAdmNotFound     = errors.New("iscsiadm command not found - please install open-iscsi")
	ErrISCSIDeviceNotFound  = errors.New("iSCSI device not found")
	ErrISCSIDeviceTimeout   = errors.New("timeout waiting for iSCSI device to appear")
	ErrISCSILoginFailed     = errors.New("failed to login to iSCSI target")
	ErrISCSIDiscoveryFailed = errors.New("iSCSI discovery failed - iscsid may not be running or accessible")
	ErrISCSITargetNotInDB   = errors.New("iSCSI target not found in node database after discovery")
)

// defaultISCSIMountOptions are sensible defaults for iSCSI filesystem mounts.
var defaultISCSIMountOptions = []string{"noatime", "_netdev"}

// iscsiadmCmd builds a command to run iscsiadm, using nsenter to execute
// in the host's namespaces when running in a container. This allows the
// container to use the host's iscsid daemon.
func iscsiadmCmd(ctx context.Context, args ...string) *exec.Cmd {
	// Check if we're in a container by looking for /proc/1/ns/mnt
	// If accessible and we have hostPID, use nsenter to run in host namespace
	if _, err := os.Stat("/proc/1/ns/mnt"); err == nil {
		// Use nsenter to enter host's mount namespace (for /etc/iscsi, /run)
		// and IPC namespace (for iscsid communication)
		nsenterArgs := make([]string, 0, 4+len(args))
		nsenterArgs = append(nsenterArgs, "--mount=/proc/1/ns/mnt", "--ipc=/proc/1/ns/ipc", "--", "iscsiadm")
		nsenterArgs = append(nsenterArgs, args...)
		klog.V(5).Infof("Running iscsiadm via nsenter: nsenter %v", nsenterArgs)
		//nolint:gosec // G204: nsenter args are controlled (iscsiadm with CSI-provided params)
		return exec.CommandContext(ctx, "nsenter", nsenterArgs...)
	}

	// Not in container or no access to host namespaces - run directly
	klog.V(5).Infof("Running iscsiadm directly: iscsiadm %v", args)
	return exec.CommandContext(ctx, "iscsiadm", args...)
}

// iscsiConnectionParams holds validated iSCSI connection parameters.
type iscsiConnectionParams struct {
	iqn    string
	server string
	port   string
	lun    int
}

// stageISCSIVolume stages an iSCSI volume by logging into the target.
// It uses a retry mechanism to handle transient device stability issues.
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

	// Retry parameters for handling device stability issues
	const (
		maxRetries = 3
		retryDelay = 5 * time.Second
	)

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			klog.Infof("iSCSI staging attempt %d/%d for volume %s", attempt, maxRetries, volumeID)
		}

		// Discover and login to iSCSI target
		if loginErr := s.loginISCSITarget(ctx, params); loginErr != nil {
			lastErr = loginErr
			klog.Warningf("iSCSI login attempt %d failed: %v", attempt, loginErr)
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		// Wait for device to appear
		devicePath, err := s.waitForISCSIDevice(ctx, params, 30*time.Second)
		if err != nil {
			lastErr = err
			klog.Warningf("iSCSI device wait failed on attempt %d: %v", attempt, err)
			// Cleanup: logout before retry
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after device wait failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		klog.V(4).Infof("iSCSI device connected at %s (IQN: %s, LUN: %d, dataset: %s) on attempt %d",
			devicePath, params.iqn, params.lun, datasetName, attempt)

		// Try staging - if device becomes unavailable during staging, retry the whole connection
		stageResp, stageErr := s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
		if stageErr == nil {
			return stageResp, nil
		}

		// Check if this is a retryable error (device disappeared during staging)
		if status.Code(stageErr) == codes.Unavailable {
			lastErr = stageErr
			klog.Warningf("iSCSI staging failed on attempt %d (device unstable): %v", attempt, stageErr)
			// Logout and retry - the device may have become stale
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after staging failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		// Non-retryable error - fail immediately
		return nil, stageErr
	}

	// All retries exhausted
	return nil, status.Errorf(codes.Internal, "Failed to stage iSCSI volume after %d attempts: %v", maxRetries, lastErr)
}

// validateISCSIParams validates and extracts iSCSI connection parameters from volume context.
func (s *NodeService) validateISCSIParams(volumeContext map[string]string) (*iscsiConnectionParams, error) {
	params := &iscsiConnectionParams{
		iqn:    volumeContext[VolumeContextKeyISCSIIQN],
		server: volumeContext["server"],
		port:   volumeContext["port"],
		lun:    0, // Always LUN 0 with dedicated targets
	}

	// Log all volume context keys for debugging
	klog.Infof("iSCSI validateISCSIParams - volume context keys: %v", volumeContext)
	klog.Infof("iSCSI validateISCSIParams - extracted IQN: '%s', server: '%s', port: '%s'",
		params.iqn, params.server, params.port)

	if params.iqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "iSCSI IQN and server must be provided in volume context")
	}

	// Default port
	if params.port == "" {
		params.port = "3260"
	}

	return params, nil
}

// checkISCSIAdm checks if iscsiadm is available (either directly or via nsenter).
func (s *NodeService) checkISCSIAdm(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := iscsiadmCmd(checkCtx, "--version")
	if err := cmd.Run(); err != nil {
		return ErrISCSIAdmNotFound
	}
	return nil
}

// loginISCSITarget discovers and logs into an iSCSI target.
func (s *NodeService) loginISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	portal := params.server + ":" + params.port

	// Step 1: Discovery
	klog.Infof("iSCSI: Discovering targets at portal %s for IQN %s", portal, params.iqn)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 30*time.Second)
	defer discoverCancel()

	discoverCmd := iscsiadmCmd(discoverCtx, "-m", "discovery", "-t", "sendtargets", "-p", portal)
	output, err := discoverCmd.CombinedOutput()
	if err != nil {
		// Log the discovery error - this is critical for debugging
		klog.Errorf("iSCSI discovery failed at %s: %v, output: %s", portal, err, string(output))
		// Check if it's a connection error to iscsid
		if strings.Contains(string(output), "connect") || strings.Contains(string(output), "Connection refused") {
			return fmt.Errorf("%w: %s", ErrISCSIDiscoveryFailed, string(output))
		}
		// Continue anyway - target might already be known from previous discovery
		klog.Warningf("Continuing despite discovery failure - target may already be known")
	} else {
		klog.Infof("iSCSI discovery successful at %s, discovered targets:\n%s", portal, string(output))
	}

	// Step 2: Check if target is in node database
	// Note: Don't specify portal here because TrueNAS may report a different portal IP
	// than the hostname we used for discovery (e.g., discovery with hostname, but TrueNAS
	// reports its IP). The node database stores the portal from the discovery response.
	klog.Infof("iSCSI: Checking if target '%s' is in node database", params.iqn)
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	checkCmd := iscsiadmCmd(checkCtx, "-m", "node", "-T", params.iqn)
	klog.Infof("iSCSI: Running node check command: iscsiadm -m node -T %s", params.iqn)
	checkOutput, checkErr := checkCmd.CombinedOutput()
	if checkErr != nil {
		klog.Errorf("iSCSI target '%s' not found in node database: %v, output: %s",
			params.iqn, checkErr, string(checkOutput))
		return fmt.Errorf("%w - check that TrueNAS iSCSI service is running and target is properly configured: %s", ErrISCSITargetNotInDB, string(checkOutput))
	}
	klog.Infof("iSCSI target '%s' found in node database: %s", params.iqn, string(checkOutput))

	// Step 3: Login
	// Don't specify portal - login to the target on whatever portal it was discovered
	klog.Infof("Logging into iSCSI target: %s", params.iqn)
	loginCtx, loginCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loginCancel()

	loginCmd := iscsiadmCmd(loginCtx, "-m", "node", "-T", params.iqn, "--login")
	output, err = loginCmd.CombinedOutput()
	if err != nil {
		// Check if already logged in
		alreadyLoggedIn := strings.Contains(string(output), "already present") ||
			strings.Contains(string(output), "session already exists")
		if alreadyLoggedIn {
			klog.V(4).Infof("iSCSI target already logged in: %s", params.iqn)
			return nil
		}
		klog.Errorf("iSCSI login failed for target %s: %v, output: %s", params.iqn, err, string(output))
		return fmt.Errorf("%w: %s", ErrISCSILoginFailed, string(output))
	}

	klog.Infof("Successfully logged into iSCSI target: %s, output: %s", params.iqn, string(output))
	return nil
}

// logoutISCSITarget logs out from an iSCSI target.
func (s *NodeService) logoutISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	klog.V(4).Infof("Logging out from iSCSI target: %s", params.iqn)
	logoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Don't specify portal - logout from target on all portals
	cmd := iscsiadmCmd(logoutCtx, "-m", "node", "-T", params.iqn, "--logout")
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
	// Query active sessions with detail level 3 to see attached devices
	sessionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := iscsiadmCmd(sessionCtx, "-m", "session", "-P", "3")
	output, err := cmd.CombinedOutput()

	// Always log the output for debugging
	klog.Infof("iscsiadm -m session -P 3: err=%v, output:\n%s", err, string(output))

	if err != nil {
		return "", ErrISCSIDeviceNotFound
	}

	deviceName := parseISCSISessionDevice(string(output), params.iqn)
	if deviceName == "" {
		klog.Infof("parseISCSISessionDevice found no device for IQN: %s", params.iqn)
		return "", ErrISCSIDeviceNotFound
	}

	devicePath := "/dev/" + deviceName
	klog.Infof("Found iSCSI device: %s", devicePath)
	return devicePath, nil
}

// parseISCSISessionDevice parses iscsiadm -m session -P 3 output to find
// the attached disk for a specific IQN.
func parseISCSISessionDevice(output, targetIQN string) string {
	lines := strings.Split(output, "\n")
	inTargetSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check if we're entering a target section
		// Format: "Target: iqn.2005-10.org.freenas.ctl:pvc-xxx (non-flash)"
		// The IQN might be followed by extra text like "(non-flash)"
		if strings.HasPrefix(line, "Target:") {
			targetLine := strings.TrimPrefix(line, "Target:")
			targetLine = strings.TrimSpace(targetLine)
			// Check if this line contains our target IQN (use Contains/HasPrefix
			// because there might be extra text after the IQN)
			inTargetSection = strings.HasPrefix(targetLine, targetIQN)
			continue
		}

		// If we're in the right target section, look for attached disk
		if inTargetSection && strings.Contains(line, "Attached scsi disk") {
			// Line format: "Attached scsi disk sda	State: running"
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "disk" && i+1 < len(parts) {
					return parts[i+1] // Return device name like "sda"
				}
			}
		}
	}

	return ""
}

// waitForISCSIDevice waits for the iSCSI device to appear after login.
func (s *NodeService) waitForISCSIDevice(ctx context.Context, params *iscsiConnectionParams, timeout time.Duration) (string, error) {
	klog.Infof("Waiting for iSCSI device for IQN %s (timeout: %v)", params.iqn, timeout)

	deadline := time.Now().Add(timeout)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		devicePath, err := s.findISCSIDevice(ctx, params)
		if err == nil && devicePath != "" {
			if _, statErr := os.Stat(devicePath); statErr == nil {
				klog.Infof("iSCSI device ready: %s (attempt %d)", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(2 * time.Second)
	}

	return "", ErrISCSIDeviceTimeout
}

// stageISCSIDevice stages an iSCSI device as either block or filesystem volume.
func (s *NodeService) stageISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// Verify device still exists before proceeding (it may have disappeared due to race conditions
	// with previous volume cleanup or iSCSI session issues)
	if _, err := os.Stat(devicePath); err != nil {
		klog.Warningf("iSCSI device %s disappeared before staging could complete: %v", devicePath, err)
		return nil, status.Errorf(codes.Unavailable,
			"iSCSI device %s became unavailable: %v", devicePath, err)
	}

	// For filesystem volumes, wait for device to be fully initialized
	if !isBlockVolume {
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			// Check if device disappeared during initialization
			if _, statErr := os.Stat(devicePath); statErr != nil {
				return nil, status.Errorf(codes.Unavailable,
					"iSCSI device %s became unavailable during initialization: %v", devicePath, err)
			}
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

		// Verify device still exists after stabilization
		if _, err := os.Stat(devicePath); err != nil {
			klog.Warningf("iSCSI device %s disappeared after stabilization: %v", devicePath, err)
			return nil, status.Errorf(codes.Unavailable,
				"iSCSI device %s became unavailable after stabilization: %v", devicePath, err)
		}
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
