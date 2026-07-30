package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	ErrISCSIDeviceAmbiguous = errors.New("multiple iSCSI devices found")
	ErrISCSIDeviceInvalid   = errors.New("invalid iSCSI device state")
	ErrISCSINotBlockDevice  = errors.New("iSCSI device is not a block device")
	ErrISCSIDeviceTimeout   = errors.New("timeout waiting for iSCSI device to appear")
	ErrISCSILoginFailed     = errors.New("failed to login to iSCSI target")
	ErrISCSIDiscoveryFailed = errors.New("iSCSI discovery failed - iscsid may not be running or accessible")
	ErrISCSITargetNotInDB   = errors.New("iSCSI target not found in node database after discovery")
)

// defaultISCSIMountOptions are sensible defaults for iSCSI filesystem mounts.
var defaultISCSIMountOptions = []string{zfsNoatime, "_netdev"}

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

type iscsiDeviceDiscoveryConfig struct {
	validateDevice     func(string) error
	sessionClassDir    string
	connectionClassDir string
	deviceDir          string
}

func defaultISCSIDeviceDiscoveryConfig() *iscsiDeviceDiscoveryConfig {
	return &iscsiDeviceDiscoveryConfig{
		validateDevice:     validateISCSIBlockDevice,
		sessionClassDir:    "/sys/class/iscsi_session",
		connectionClassDir: "/sys/class/iscsi_connection",
		deviceDir:          "/dev",
	}
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

	// Try to reuse an existing connection. Only a definitive not-found result
	// may fall through to login; ambiguous or unreadable identity state must
	// not mutate host sessions.
	devicePath, findErr := s.findISCSIDevice(ctx, params)
	if findErr == nil && devicePath != "" {
		klog.V(4).Infof("iSCSI device already connected at %s - reusing existing connection", devicePath)
		return s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	}
	if findErr != nil && !errors.Is(findErr, ErrISCSIDeviceNotFound) {
		return nil, iscsiDiscoveryStatusError(findErr)
	}

	// Check if iscsiadm is installed
	if checkErr := s.checkISCSIAdm(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "open-iscsi not available: %v", checkErr)
	}

	// Retry parameters for handling iSCSI service availability issues.
	// The iSCSI service on TrueNAS may be temporarily unavailable during
	// service reloads triggered by target creation.
	const (
		maxRetries = 5
		retryDelay = 15 * time.Second
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
			if !errors.Is(err, ErrISCSIDeviceTimeout) && !errors.Is(err, ErrISCSIDeviceNotFound) {
				return nil, iscsiDiscoveryStatusError(err)
			}
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

func iscsiDiscoveryStatusError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	return status.Errorf(codes.FailedPrecondition, "cannot safely identify iSCSI device: %v", err)
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
	config := s.iscsiDiscovery
	if config == nil ||
		config.sessionClassDir == "" ||
		config.connectionClassDir == "" ||
		config.deviceDir == "" ||
		config.validateDevice == nil {

		config = defaultISCSIDeviceDiscoveryConfig()
	}

	devicePath, err := findISCSIDeviceInSysfs(ctx, config, params)
	if err != nil {
		klog.V(4).Infof("iSCSI sysfs lookup failed for IQN %s LUN %d: %v", params.iqn, params.lun, err)
		return "", err
	}

	klog.Infof("Found iSCSI device in sysfs: %s (IQN: %s, LUN: %d)", devicePath, params.iqn, params.lun)
	return devicePath, nil
}

func findISCSIDeviceInSysfs(
	ctx context.Context,
	config *iscsiDeviceDiscoveryConfig,
	params *iscsiConnectionParams,
) (string, error) {

	if err := ctx.Err(); err != nil {
		return "", err
	}

	sessionEntries, err := os.ReadDir(config.sessionClassDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w for IQN %s: session class is not present", ErrISCSIDeviceNotFound, params.iqn)
		}
		return "", fmt.Errorf("%w: read session class %s: %w", ErrISCSIDeviceInvalid, config.sessionClassDir, err)
	}

	var matchingSessions []string
	for _, entry := range sessionEntries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if !strings.HasPrefix(entry.Name(), "session") {
			continue
		}

		sessionPath := filepath.Join(config.sessionClassDir, entry.Name())
		//nolint:gosec // The path is bounded to kernel-created sysfs session entries.
		targetName, readErr := os.ReadFile(filepath.Join(sessionPath, "targetname"))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return "", fmt.Errorf("%w: read target name for %s: %w", ErrISCSIDeviceInvalid, entry.Name(), readErr)
		}
		if strings.TrimSpace(string(targetName)) == params.iqn {
			matchingSessions = append(matchingSessions, sessionPath)
		}
	}

	if len(matchingSessions) == 0 {
		return "", fmt.Errorf("%w for IQN %s", ErrISCSIDeviceNotFound, params.iqn)
	}
	if len(matchingSessions) > 1 {
		matchingSessions, err = filterISCSISessionsByPortal(ctx, config.connectionClassDir, matchingSessions, params)
		if err != nil {
			return "", err
		}
	}
	if len(matchingSessions) > 1 {
		return "", fmt.Errorf("%w for IQN %s: %d exact sessions", ErrISCSIDeviceAmbiguous, params.iqn, len(matchingSessions))
	}

	resolvedDeviceDir, err := filepath.EvalSymlinks(filepath.Join(matchingSessions[0], "device"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w for IQN %s: session device disappeared", ErrISCSIDeviceNotFound, params.iqn)
		}
		return "", fmt.Errorf("%w: resolve session device for IQN %s: %w", ErrISCSIDeviceInvalid, params.iqn, err)
	}

	deviceNames, err := findISCSILUNDeviceNames(ctx, resolvedDeviceDir, params.lun)
	if err != nil {
		return "", err
	}
	if len(deviceNames) == 0 {
		return "", fmt.Errorf("%w for IQN %s LUN %d", ErrISCSIDeviceNotFound, params.iqn, params.lun)
	}
	if len(deviceNames) > 1 {
		return "", fmt.Errorf("%w for IQN %s LUN %d: %v", ErrISCSIDeviceAmbiguous, params.iqn, params.lun, deviceNames)
	}

	devicePath := filepath.Join(config.deviceDir, deviceNames[0])
	if _, statErr := os.Stat(devicePath); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", fmt.Errorf("%w for IQN %s: device node %s not ready", ErrISCSIDeviceNotFound, params.iqn, devicePath)
		}
		return "", fmt.Errorf("%w: stat device node %s: %w", ErrISCSIDeviceInvalid, devicePath, statErr)
	}
	if validateErr := config.validateDevice(devicePath); validateErr != nil {
		if os.IsNotExist(validateErr) {
			return "", fmt.Errorf("%w for IQN %s: device node %s disappeared", ErrISCSIDeviceNotFound, params.iqn, devicePath)
		}
		return "", fmt.Errorf("%w: validate device node %s: %w", ErrISCSIDeviceInvalid, devicePath, validateErr)
	}

	return devicePath, nil
}

func filterISCSISessionsByPortal(
	ctx context.Context,
	connectionClassDir string,
	sessions []string,
	params *iscsiConnectionParams,
) ([]string, error) {

	if params.server == "" || params.port == "" {
		return sessions, nil
	}

	connectionEntries, err := os.ReadDir(connectionClassDir)
	if err != nil {
		if os.IsNotExist(err) {
			return sessions, nil
		}
		return nil, fmt.Errorf("%w: read connection class %s: %w", ErrISCSIDeviceInvalid, connectionClassDir, err)
	}

	matchingPortals := make([]string, 0, len(sessions))
	for _, sessionPath := range sessions {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		sessionNumber := strings.TrimPrefix(filepath.Base(sessionPath), "session")
		connectionPrefix := "connection" + sessionNumber + ":"
		sessionMatches := false
		for _, connectionEntry := range connectionEntries {
			if !strings.HasPrefix(connectionEntry.Name(), connectionPrefix) {
				continue
			}

			connectionPath := filepath.Join(connectionClassDir, connectionEntry.Name())
			//nolint:gosec // The path is bounded to kernel-created sysfs connection entries.
			address, readErr := os.ReadFile(filepath.Join(connectionPath, "address"))
			if readErr != nil {
				if os.IsNotExist(readErr) {
					continue
				}
				return nil, fmt.Errorf("%w: read address for %s: %w", ErrISCSIDeviceInvalid, connectionEntry.Name(), readErr)
			}
			//nolint:gosec // The path is bounded to kernel-created sysfs connection entries.
			port, readErr := os.ReadFile(filepath.Join(connectionPath, "port"))
			if readErr != nil {
				if os.IsNotExist(readErr) {
					continue
				}
				return nil, fmt.Errorf("%w: read port for %s: %w", ErrISCSIDeviceInvalid, connectionEntry.Name(), readErr)
			}

			if strings.TrimSpace(string(address)) == params.server &&
				strings.TrimSpace(string(port)) == params.port {

				sessionMatches = true
				break
			}
		}
		if sessionMatches {
			matchingPortals = append(matchingPortals, sessionPath)
		}
	}

	if len(matchingPortals) == 0 {
		return sessions, nil
	}
	return matchingPortals, nil
}

func findISCSILUNDeviceNames(ctx context.Context, sessionDeviceDir string, lun int) ([]string, error) {
	targetEntries, err := os.ReadDir(sessionDeviceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read session device directory %s: %w", ErrISCSIDeviceInvalid, sessionDeviceDir, err)
	}

	devices := make(map[string]struct{})
	for _, targetEntry := range targetEntries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !strings.HasPrefix(targetEntry.Name(), "target") {
			continue
		}

		targetPath := filepath.Join(sessionDeviceDir, targetEntry.Name())
		scsiEntries, readErr := os.ReadDir(targetPath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, fmt.Errorf("%w: read SCSI target %s: %w", ErrISCSIDeviceInvalid, targetPath, readErr)
		}

		for _, scsiEntry := range scsiEntries {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if !scsiAddressMatchesLUN(scsiEntry.Name(), lun) {
				continue
			}

			blockPath := filepath.Join(targetPath, scsiEntry.Name(), "block")
			blockEntries, readErr := os.ReadDir(blockPath)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					continue
				}
				return nil, fmt.Errorf("%w: read block devices at %s: %w", ErrISCSIDeviceInvalid, blockPath, readErr)
			}
			for _, blockEntry := range blockEntries {
				devices[blockEntry.Name()] = struct{}{}
			}
		}
	}

	deviceNames := make([]string, 0, len(devices))
	for deviceName := range devices {
		deviceNames = append(deviceNames, deviceName)
	}
	sort.Strings(deviceNames)
	return deviceNames, nil
}

func scsiAddressMatchesLUN(address string, lun int) bool {
	parts := strings.Split(address, ":")
	if len(parts) != 4 {
		return false
	}
	addressLUN, err := strconv.Atoi(parts[3])
	return err == nil && addressLUN == lun
}

func validateISCSIBlockDevice(devicePath string) error {
	info, err := os.Stat(devicePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("%w: %s", ErrISCSINotBlockDevice, devicePath)
	}
	return nil
}

// waitForISCSIDevice waits for the iSCSI device to appear after login.
func (s *NodeService) waitForISCSIDevice(ctx context.Context, params *iscsiConnectionParams, timeout time.Duration) (string, error) {
	klog.Infof("Waiting for iSCSI device for IQN %s (timeout: %v)", params.iqn, timeout)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for attempt := 1; ; attempt++ {
		devicePath, err := s.findISCSIDevice(ctx, params)
		if err == nil && devicePath != "" {
			klog.Infof("iSCSI device ready: %s (attempt %d)", devicePath, attempt)
			return devicePath, nil
		}
		if err != nil && !errors.Is(err, ErrISCSIDeviceNotFound) {
			return "", err
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", ErrISCSIDeviceTimeout
		case <-ticker.C:
		}
	}
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
