package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"github.com/fenio/tns-csi/pkg/retry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for NVMe-oF operations.
var (
	ErrNVMeCLINotFound             = errors.New("nvme command not found - please install nvme-cli")
	ErrNVMeDeviceNotFound          = errors.New("NVMe device not found")
	ErrNVMeDeviceTimeout           = errors.New("timeout waiting for NVMe device to appear")
	ErrDeviceInitializationTimeout = errors.New("device failed to initialize - size remained zero or unreadable")
	ErrNVMeControllerNotFound      = errors.New("could not extract NVMe controller path from device path")
	ErrDeviceSizeMismatch          = errors.New("device size does not match expected capacity")
)

// defaultNVMeOFMountOptions are sensible defaults for NVMe-oF filesystem mounts.
// These are merged with user-specified mount options from StorageClass.
var defaultNVMeOFMountOptions = []string{"noatime"}

// nvmeOFConnectionParams holds validated NVMe-oF connection parameters.
// With independent subsystems per volume, NSID is always 1.
type nvmeOFConnectionParams struct {
	nqn       string
	server    string
	transport string
	port      string
}

// stageNVMeOFVolume stages an NVMe-oF volume by connecting to the target.
// With independent subsystems, each volume has its own NQN and NSID is always 1.
func (s *NodeService) stageNVMeOFVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Validate and extract connection parameters
	params, err := s.validateNVMeOFParams(volumeContext)
	if err != nil {
		return nil, err
	}

	isBlockVolume := volumeCapability.GetBlock() != nil
	datasetName := volumeContext["datasetName"]
	klog.V(4).Infof("Staging NVMe-oF volume %s (block mode: %v): server=%s:%s, NQN=%s, dataset=%s",
		volumeID, isBlockVolume, params.server, params.port, params.nqn, datasetName)

	// Try to reuse existing connection (idempotent staging)
	if resp, _, reuseErr := s.tryReuseExistingConnection(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext); reuseErr != nil {
		return nil, reuseErr
	} else if resp != nil {
		return resp, nil
	}

	// Check if nvme-cli is installed
	if checkErr := s.checkNVMeCLI(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "nvme-cli not available: %v", checkErr)
	}

	// Connect to NVMe-oF target and stage device
	return s.connectAndStageDevice(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext, datasetName)
}

// tryReuseExistingConnection attempts to reuse an existing NVMe-oF connection.
// Returns the response if successful, or nil if no existing connection found.
// With independent subsystems, we simply check if the device for this NQN exists.
func (s *NodeService) tryReuseExistingConnection(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (resp *csi.NodeStageVolumeResponse, devicePath string, err error) {
	// With independent subsystems, NSID is always 1
	devicePath, findErr := s.findNVMeDeviceByNQN(ctx, params.nqn)
	if findErr != nil || devicePath == "" {
		// Device not found is expected when not previously connected - return nil to try other methods
		return nil, "", nil //nolint:nilerr // intentionally swallowing "device not found" as this is expected
	}

	klog.V(4).Infof("NVMe-oF device already connected at %s for NQN=%s - checking if connection is healthy",
		devicePath, params.nqn)

	// Rescan the namespace to ensure we have fresh data from the target
	if rescanErr := s.rescanNVMeNamespace(ctx, devicePath); rescanErr != nil {
		klog.Warningf("Failed to rescan NVMe namespace %s: %v (continuing anyway)", devicePath, rescanErr)
	}

	// Verify the existing connection is healthy by checking device size
	// A stale connection may have the device file but report zero size
	if healthy := s.verifyDeviceHealthy(ctx, devicePath); !healthy {
		klog.Warningf("Existing NVMe device %s appears stale (zero size) - disconnecting to force reconnect", devicePath)
		if disconnectErr := s.disconnectNVMeOF(ctx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect stale NVMe-oF connection: %v", disconnectErr)
		}
		// Return nil to trigger a full reconnect
		return nil, "", nil
	}

	klog.V(4).Infof("Existing NVMe-oF device %s is healthy - reusing connection (idempotent)", devicePath)

	// Proceed directly to staging with the existing device
	resp, err = s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	if err != nil {
		klog.Errorf("Failed to stage existing NVMe device: %v", err)
		return nil, devicePath, err
	}
	return resp, devicePath, nil
}

// verifyDeviceHealthy checks if an NVMe device is healthy by verifying it reports a non-zero size.
// Returns true if the device is healthy, false if it appears stale or broken.
func (s *NodeService) verifyDeviceHealthy(ctx context.Context, devicePath string) bool {
	const (
		maxAttempts   = 5                      // Quick check, don't wait too long
		checkInterval = 500 * time.Millisecond // Half second between checks
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		sizeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
		output, err := cmd.CombinedOutput()
		cancel()

		if err == nil {
			sizeStr := strings.TrimSpace(string(output))
			if size, parseErr := strconv.ParseInt(sizeStr, 10, 64); parseErr == nil && size > 0 {
				klog.V(4).Infof("Device %s health check passed: size=%d bytes (attempt %d)", devicePath, size, attempt)
				return true
			}
			klog.V(4).Infof("Device %s health check attempt %d/%d: size=%s (zero)", devicePath, attempt, maxAttempts, sizeStr)
		} else {
			klog.V(4).Infof("Device %s health check attempt %d/%d failed: %v", devicePath, attempt, maxAttempts, err)
		}

		if attempt < maxAttempts {
			time.Sleep(checkInterval)
		}
	}

	klog.V(4).Infof("Device %s failed health check after %d attempts (size remained zero)", devicePath, maxAttempts)
	return false
}

// connectAndStageDevice connects to the NVMe-oF target and stages the device.
// If the device doesn't appear after the first attempt, it will disconnect and retry.
func (s *NodeService) connectAndStageDevice(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string, datasetName string) (*csi.NodeStageVolumeResponse, error) {
	const (
		deviceWaitTimeout = 45 * time.Second // Wait up to 45s for device to appear
		maxConnectRetries = 2                // Try up to 2 connect cycles
	)

	var lastErr error
	for attempt := 1; attempt <= maxConnectRetries; attempt++ {
		if attempt > 1 {
			klog.V(4).Infof("Retrying NVMe-oF connection (attempt %d/%d) for NQN: %s", attempt, maxConnectRetries, params.nqn)
		}

		// Connect to NVMe-oF target (this handles both new connections and retries)
		if connectErr := s.connectNVMeOFTarget(ctx, params); connectErr != nil {
			lastErr = connectErr
			klog.Warningf("NVMe-oF connect attempt %d failed: %v", attempt, connectErr)
			continue
		}

		// Wait for device to appear (NSID is always 1 with independent subsystems)
		devicePath, err := s.waitForNVMeDevice(ctx, params.nqn, deviceWaitTimeout)
		if err == nil {
			klog.V(4).Infof("NVMe-oF device connected at %s (NQN: %s, dataset: %s) on attempt %d",
				devicePath, params.nqn, datasetName, attempt)
			return s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
		}

		lastErr = err
		klog.Warningf("NVMe-oF device wait failed on attempt %d: %v", attempt, err)

		// Disconnect before retry (or final cleanup)
		if disconnectErr := s.disconnectNVMeOF(ctx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect NVMe-oF after device wait failure: %v", disconnectErr)
		}

		// Small delay before retry to let things settle
		if attempt < maxConnectRetries {
			klog.V(4).Infof("Waiting 3s before retry...")
			time.Sleep(3 * time.Second)
		}
	}

	return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after %d connection attempts (NQN: %s): %v",
		maxConnectRetries, params.nqn, lastErr)
}

// validateNVMeOFParams validates and extracts NVMe-oF connection parameters from volume context.
// With independent subsystems, nsid is not required (always 1).
func (s *NodeService) validateNVMeOFParams(volumeContext map[string]string) (*nvmeOFConnectionParams, error) {
	params := &nvmeOFConnectionParams{
		nqn:       volumeContext["nqn"],
		server:    volumeContext["server"],
		transport: volumeContext["transport"],
		port:      volumeContext["port"],
	}

	if params.nqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "nqn and server must be provided in volume context for NVMe-oF volumes")
	}

	// Default values
	if params.transport == "" {
		params.transport = "tcp"
	}
	if params.port == "" {
		params.port = "4420"
	}

	return params, nil
}

// connectNVMeOFTarget discovers and connects to an NVMe-oF target with retry logic.
// This handles transient failures when TrueNAS has just created a new subsystem
// (e.g., for snapshot-restored volumes) but it's not yet fully ready for connections.
func (s *NodeService) connectNVMeOFTarget(ctx context.Context, params *nvmeOFConnectionParams) error {
	// Discover the NVMe-oF target
	klog.V(4).Infof("Discovering NVMe-oF target at %s:%s", params.server, params.port)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
	defer discoverCancel()
	//nolint:gosec // nvme discover with volume context variables is expected for CSI driver
	discoverCmd := exec.CommandContext(discoverCtx, "nvme", "discover", "-t", params.transport, "-a", params.server, "-s", params.port)
	if output, discoverErr := discoverCmd.CombinedOutput(); discoverErr != nil {
		klog.Warningf("NVMe discover failed (this may be OK if target is already known): %v, output: %s", discoverErr, string(output))
	}

	// Connect to the NVMe-oF target with retry logic
	// This is necessary because newly created subsystems (e.g., from snapshot restore)
	// may not be immediately ready for connections on TrueNAS
	klog.V(4).Infof("Connecting to NVMe-oF target: %s", params.nqn)

	config := retry.Config{
		MaxAttempts:       6,               // Up to 6 attempts
		InitialBackoff:    2 * time.Second, // Start with 2s delay
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 1.5,
		RetryableFunc:     isRetryableNVMeConnectError,
		OperationName:     fmt.Sprintf("nvme-connect(%s)", params.nqn),
	}

	if err := retry.WithRetryNoResult(ctx, config, func() error {
		return s.attemptNVMeConnect(ctx, params)
	}); err != nil {
		return err
	}

	// After successful connection, trigger udev to process new NVMe devices.
	// This helps ensure the kernel and udev properly enumerate the newly connected device,
	// reducing the chance of device discovery timeouts.
	triggerUdevForNVMeSubsystem(ctx)

	return nil
}

// attemptNVMeConnect performs a single NVMe connect attempt.
func (s *NodeService) attemptNVMeConnect(ctx context.Context, params *nvmeOFConnectionParams) error {
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connectCancel()

	// NVMe-oF connection with resilience and performance options:
	// --reconnect-delay=2: Wait 2 seconds before reconnecting after connection loss
	// --ctrl-loss-tmo=60: Keep retrying for 60 seconds before giving up
	// --keep-alive-tmo=5: Send keepalive every 5 seconds to detect dead connections
	// --nr-io-queues=4: Use 4 I/O queues for better concurrency under load
	//nolint:gosec // nvme connect with volume context variables is expected for CSI driver
	connectCmd := exec.CommandContext(connectCtx, "nvme", "connect",
		"-t", params.transport,
		"-n", params.nqn,
		"-a", params.server,
		"-s", params.port,
		"--reconnect-delay=2",
		"--ctrl-loss-tmo=60",
		"--keep-alive-tmo=5",
		"--nr-io-queues=4",
	)
	output, err := connectCmd.CombinedOutput()
	if err != nil {
		// Check if already connected (this is success, not an error)
		if strings.Contains(string(output), "already connected") {
			klog.V(4).Infof("NVMe device already connected (output: %s)", string(output))
			return nil
		}
		return fmt.Errorf("nvme connect failed: %w, output: %s", err, string(output))
	}

	return nil
}

// isRetryableNVMeConnectError determines if an NVMe connect error is transient
// and should be retried. This includes errors from newly created subsystems
// that aren't fully initialized on TrueNAS yet.
func isRetryableNVMeConnectError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// These errors indicate the subsystem isn't ready yet (transient)
	retryablePatterns := []string{
		"failed to write to nvme-fabrics device", // Subsystem not yet accepting connections
		"could not add new controller",           // Controller registration pending
		"connection refused",                     // Target not listening yet
		"connection timed out",                   // Target slow to respond
		"No route to host",                       // Network path not ready
		"Host is down",                           // Target initializing
		"Network is unreachable",                 // Transient network issue
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// triggerUdevForNVMeSubsystem triggers udev to process new NVMe devices after a connection.
// This helps ensure the kernel and udev properly enumerate newly connected NVMe-oF devices.
func triggerUdevForNVMeSubsystem(ctx context.Context) {
	klog.V(4).Infof("Triggering udev to process new NVMe devices")

	// Trigger udev to process any new NVMe devices
	triggerCtx, triggerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer triggerCancel()
	triggerCmd := exec.CommandContext(triggerCtx, "udevadm", "trigger", "--action=add", "--subsystem-match=nvme")
	if output, err := triggerCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger for NVMe subsystem failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev add events for NVMe subsystem")
	}

	// Also trigger block subsystem in case block devices need processing
	blockTriggerCtx, blockTriggerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer blockTriggerCancel()
	blockTriggerCmd := exec.CommandContext(blockTriggerCtx, "udevadm", "trigger", "--action=add", "--subsystem-match=block")
	if output, err := blockTriggerCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger for block subsystem failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev add events for block subsystem")
	}

	// Wait for udev to settle (process the events)
	settleCtx, settleCancel := context.WithTimeout(ctx, 15*time.Second)
	defer settleCancel()
	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=10")
	if output, err := settleCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm settle failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("udevadm settle completed after NVMe connection")
	}
}

// verifyNVMeSubsystemConnected verifies that an NVMe subsystem is connected using nvme list-subsys.
// This is useful to confirm the kernel has registered the connection before looking for devices.
func verifyNVMeSubsystemConnected(ctx context.Context, nqn string) bool {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Use nvme list-subsys to get all connected subsystems
	cmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, output: %s", err, string(output))
		return false
	}

	// Simple string search for the NQN in the output
	// The JSON output contains "NQN" : "nqn.xxx" entries
	if strings.Contains(string(output), nqn) {
		klog.V(4).Infof("Verified NVMe subsystem %s is connected (found in nvme list-subsys)", nqn)
		return true
	}

	klog.V(4).Infof("NVMe subsystem %s not yet visible in nvme list-subsys", nqn)
	return false
}

// waitForDeviceInitialization waits for an NVMe device to be fully initialized.
// A device is considered initialized when it reports a non-zero size.
func waitForDeviceInitialization(ctx context.Context, devicePath string) error {
	const (
		maxAttempts   = 45               // 45 attempts
		checkInterval = 1 * time.Second  // 1 second between checks
		totalTimeout  = 60 * time.Second // Maximum wait time (increased for concurrent mounts)
	)

	klog.V(4).Infof("Waiting for device %s to be fully initialized (non-zero size)", devicePath)

	// Create a context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	for attempt := range maxAttempts {
		// Check if context is canceled
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("%w for device %s: %w", ErrDeviceInitializationTimeout, devicePath, timeoutCtx.Err())
		default:
		}

		// Get device size using blockdev
		sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
		cmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
		output, err := cmd.CombinedOutput()
		sizeCancel()

		if err == nil {
			sizeStr := strings.TrimSpace(string(output))
			if size, parseErr := strconv.ParseInt(sizeStr, 10, 64); parseErr == nil && size > 0 {
				klog.V(4).Infof("Device %s initialized successfully with size %d bytes (after %d attempts)", devicePath, size, attempt+1)
				return nil
			}
			klog.V(4).Infof("Device %s size check attempt %d/%d: size=%s (waiting for non-zero)", devicePath, attempt+1, maxAttempts, sizeStr)
		} else {
			klog.V(4).Infof("Device %s size check attempt %d/%d failed: %v (device may not be ready yet)", devicePath, attempt+1, maxAttempts, err)
		}

		// Wait before next attempt (unless this is the last attempt)
		if attempt < maxAttempts-1 {
			select {
			case <-time.After(checkInterval):
			case <-timeoutCtx.Done():
				return fmt.Errorf("%w for device %s: %w", ErrDeviceInitializationTimeout, devicePath, timeoutCtx.Err())
			}
		}
	}

	return ErrDeviceInitializationTimeout
}

// stageNVMeDevice stages an NVMe device as either block or filesystem volume.
func (s *NodeService) stageNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// For filesystem volumes, wait for device to be fully initialized.
	if !isBlockVolume {
		// First, wait for device to report non-zero size (indicates device is initialized)
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			return nil, status.Errorf(codes.Internal, "Device initialization timeout: %v", err)
		}

		// Force the kernel to completely re-read the device identity
		if err := forceDeviceRescan(ctx, devicePath); err != nil {
			klog.Warningf("Device rescan warning for %s: %v (continuing anyway)", devicePath, err)
		}

		// Additional stabilization delay to ensure metadata is readable after rescan
		const deviceMetadataDelay = 2 * time.Second
		klog.V(4).Infof("Waiting %v for device %s metadata to stabilize after rescan", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)
		klog.V(4).Infof("Device metadata stabilization delay complete for %s", devicePath)
	}

	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, volumeContext)
}

// unstageNVMeOFVolume unstages an NVMe-oF volume by disconnecting from the target.
// With independent subsystems, we always disconnect when unstaging (no shared subsystem check needed).
func (s *NodeService) unstageNVMeOFVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging NVMe-oF volume %s from %s", volumeID, stagingTargetPath)

	// Get NQN from volume context
	nqn := volumeContext["nqn"]

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

	// If we don't have NQN, we can't disconnect
	if nqn == "" {
		klog.Warningf("Cannot determine NQN for volume %s - skipping NVMe-oF disconnect", volumeID)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// With independent subsystems, always disconnect (no shared subsystem to worry about)
	klog.V(4).Infof("Disconnecting NVMe-oF subsystem for volume %s: NQN=%s", volumeID, nqn)
	if err := s.disconnectNVMeOF(ctx, nqn); err != nil {
		klog.Warningf("Failed to disconnect NVMe-oF device (continuing anyway): %v", err)
	} else {
		klog.V(4).Infof("Disconnected from NVMe-oF target: %s", nqn)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// formatAndMountNVMeDevice formats (if needed) and mounts an NVMe device.
func (s *NodeService) formatAndMountNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	nqn := volumeContext["nqn"]
	klog.V(4).Infof("Formatting and mounting NVMe device: device=%s, path=%s, volume=%s, dataset=%s, NQN=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, nqn)

	// Log device information for troubleshooting
	s.logDeviceInfo(ctx, devicePath)

	// SAFETY CHECK: Verify device size matches expected capacity
	if err := s.verifyDeviceSize(ctx, devicePath, volumeContext); err != nil {
		klog.Errorf("Device size verification FAILED for %s: %v", devicePath, err)
		return nil, status.Errorf(codes.FailedPrecondition,
			"Device size mismatch detected - refusing to mount to prevent data corruption: %v", err)
	}

	// Determine filesystem type from volume capability
	fsType := "ext4" // default
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Check if this volume was cloned from a snapshot
	isClone := false
	if cloned, exists := volumeContext[VolumeContextKeyClonedFromSnap]; exists && cloned == VolumeContextValueTrue {
		isClone = true
		klog.V(4).Infof("Volume %s was cloned from snapshot - adding extra stabilization delay before filesystem check", volumeID)
		// Reduced delay with independent subsystems (no NSID cache pollution)
		const cloneStabilizationDelay = 5 * time.Second
		klog.V(4).Infof("Waiting %v for cloned volume %s filesystem metadata to stabilize", cloneStabilizationDelay, devicePath)
		time.Sleep(cloneStabilizationDelay)
		klog.V(4).Infof("Clone stabilization delay complete for %s", devicePath)
	}

	// Check if device needs formatting (will detect existing filesystem or format if needed)
	if err := s.handleDeviceFormatting(ctx, volumeID, devicePath, fsType, datasetName, nqn, isClone); err != nil {
		return nil, err
	}

	// Create staging target path if it doesn't exist
	if mkdirErr := os.MkdirAll(stagingTargetPath, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", mkdirErr)
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

	// Get user-specified mount options from StorageClass (passed via VolumeCapability)
	var userMountOptions []string
	if mnt := volumeCapability.GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getNVMeOFMountOptions(userMountOptions)

	klog.V(4).Infof("NVMe-oF mount options: user=%v, final=%v", userMountOptions, mountOptions)

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

	klog.V(4).Infof("Mounted NVMe device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

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
// With independent subsystems, NSID is always 1, so we just need to find the controller
// and return the n1 device.
func (s *NodeService) findNVMeDeviceByNQN(ctx context.Context, nqn string) (string, error) {
	klog.V(4).Infof("Searching for NVMe device: NQN=%s (NSID=1)", nqn)

	// Use nvme list-subsys which shows NQN
	subsysOutput, err := s.runNVMeListSubsys(ctx)
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, falling back to sysfs", err)
		return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
	}

	// Try to parse the output and find the device
	devicePath := s.parseNVMeListSubsysOutputForNQN(subsysOutput, nqn)
	if devicePath != "" {
		return devicePath, nil
	}

	// Fall back to checking /sys/class/nvme if parsing failed
	return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
}

// runNVMeListSubsys executes nvme list-subsys and returns the output.
func (s *NodeService) runNVMeListSubsys(ctx context.Context) ([]byte, error) {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	return subsysCmd.CombinedOutput()
}

// parseNVMeListSubsysOutputForNQN parses nvme list-subsys JSON output to find device path.
// With independent subsystems, NSID is always 1.
func (s *NodeService) parseNVMeListSubsysOutputForNQN(output []byte, nqn string) string {
	lines := strings.Split(string(output), "\n")
	foundNQN := false

	for i, line := range lines {
		if !strings.Contains(line, nqn) {
			continue
		}

		foundNQN = true
		devicePath := s.extractDevicePathFromLinesForNQN(lines, i, nqn)
		if devicePath != "" {
			return devicePath
		}
	}

	if foundNQN {
		klog.Warningf("Found NQN but could not extract device name, falling back to sysfs")
	}
	return ""
}

// extractDevicePathFromLinesForNQN searches for controller name in lines after the NQN line.
// With independent subsystems, NSID is always 1.
func (s *NodeService) extractDevicePathFromLinesForNQN(lines []string, startIdx int, nqn string) string {
	// Look ahead for the "Name" field in the Paths section (up to 20 lines)
	endIdx := startIdx + 20
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	for j := startIdx; j < endIdx; j++ {
		if !strings.Contains(lines[j], "\"Name\"") || !strings.Contains(lines[j], "nvme") {
			continue
		}

		// Extract controller name - format: "Name" : "nvme0"
		parts := strings.Split(lines[j], "\"")
		controllerName := s.extractControllerFromParts(parts)
		if controllerName == "" {
			continue
		}

		// With independent subsystems, NSID is always 1
		devicePath := fmt.Sprintf("/dev/%sn1", controllerName)
		klog.V(4).Infof("Found NVMe device from list-subsys: %s (controller: %s, NQN: %s)",
			devicePath, controllerName, nqn)
		return devicePath
	}
	return ""
}

// extractControllerFromParts extracts controller name from parsed JSON parts.
func (s *NodeService) extractControllerFromParts(parts []string) string {
	for k := range len(parts) - 1 {
		if parts[k] == "Name" && k+2 < len(parts) {
			return strings.TrimSpace(parts[k+2])
		}
	}
	return ""
}

// findNVMeDeviceByNQNFromSys finds NVMe device by checking /sys/class/nvme.
// With independent subsystems, NSID is always 1.
func (s *NodeService) findNVMeDeviceByNQNFromSys(ctx context.Context, nqn string) (string, error) {
	klog.V(4).Infof("Searching for NVMe device via sysfs: NQN=%s (NSID=1)", nqn)

	// Read /sys/class/nvme/nvmeX/subsysnqn for each device
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", nvmeDir, err)
	}

	klog.V(4).Infof("Found %d NVMe controller(s) in sysfs", len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		deviceName := entry.Name()
		// Skip non-controller entries
		if !strings.HasPrefix(deviceName, "nvme") || strings.Contains(deviceName, "-") {
			continue
		}
		// Skip namespace entries (like nvme0n1)
		if strings.Contains(deviceName[4:], "n") {
			continue
		}

		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")

		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(5).Infof("Cannot read NQN for %s: %v", deviceName, err)
			continue
		}

		deviceNQN := strings.TrimSpace(string(data))
		if deviceNQN == nqn {
			// Found the device, construct path with NSID=1 (independent subsystems)
			devicePath := fmt.Sprintf("/dev/%sn1", deviceName)
			if _, err := os.Stat(devicePath); err == nil {
				klog.V(4).Infof("Found NVMe device from sysfs: %s (controller: %s, NQN: %s)",
					devicePath, deviceName, nqn)
				return devicePath, nil
			}
			// Controller exists but namespace device doesn't - try ns-rescan
			controllerPath := "/dev/" + deviceName
			klog.V(4).Infof("Found matching NQN on %s but device path %s does not exist, trying ns-rescan", deviceName, devicePath)
			s.forceNamespaceRescan(ctx, controllerPath)
			// Check again after rescan
			if _, err := os.Stat(devicePath); err == nil {
				klog.V(4).Infof("Found NVMe device after ns-rescan: %s (controller: %s, NQN: %s)",
					devicePath, deviceName, nqn)
				return devicePath, nil
			}
			klog.Warningf("Device path %s still does not exist after ns-rescan", devicePath)
		} else {
			klog.V(5).Infof("Controller %s has different NQN: %s (looking for: %s)", deviceName, deviceNQN, nqn)
		}
	}

	klog.Warningf("NVMe device not found in sysfs for NQN=%s", nqn)
	return "", fmt.Errorf("%w for NQN: %s", ErrNVMeDeviceNotFound, nqn)
}

// forceNamespaceRescan forces the kernel to rescan namespaces on an NVMe controller.
func (s *NodeService) forceNamespaceRescan(ctx context.Context, controllerPath string) {
	rescanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	klog.V(4).Infof("Forcing namespace rescan on controller %s", controllerPath)

	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme ns-rescan failed for %s: %v, output: %s (continuing anyway)", controllerPath, err, string(output))
	} else {
		klog.V(4).Infof("nvme ns-rescan completed for %s", controllerPath)
	}

	// Also trigger udev after rescan
	triggerUdevForNVMeSubsystem(ctx)
}

// waitForNVMeDevice waits for the NVMe device to appear after connection.
// With independent subsystems, NSID is always 1.
func (s *NodeService) waitForNVMeDevice(ctx context.Context, nqn string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0
	subsystemVerified := false

	for time.Now().Before(deadline) {
		attempt++

		// First, verify the subsystem is registered with the kernel (using nvme list-subsys)
		// This helps diagnose issues where nvme connect succeeded but the subsystem isn't visible
		if !subsystemVerified {
			if verifyNVMeSubsystemConnected(ctx, nqn) {
				subsystemVerified = true
				klog.V(4).Infof("NVMe subsystem %s verified as connected on attempt %d", nqn, attempt)
			} else if attempt%5 == 0 {
				// Every 5 attempts, log that we're still waiting for subsystem
				klog.V(4).Infof("Still waiting for NVMe subsystem %s to appear in kernel (attempt %d)", nqn, attempt)
				// Re-trigger udev in case it missed the initial events
				triggerUdevForNVMeSubsystem(ctx)
			}
		}

		devicePath, err := s.findNVMeDeviceByNQN(ctx, nqn)
		if err == nil && devicePath != "" {
			// Verify device is accessible AND healthy (non-zero size)
			// This prevents returning a device that exists but isn't functional yet
			if _, statErr := os.Stat(devicePath); statErr == nil {
				if s.isDeviceHealthy(ctx, devicePath) {
					klog.V(4).Infof("NVMe device found and healthy at %s after %d attempts (subsystem verified: %v)", devicePath, attempt, subsystemVerified)
					return devicePath, nil
				}
				klog.V(4).Infof("Device %s exists but reports zero size, waiting for initialization (attempt %d)", devicePath, attempt)
			}
		}
		time.Sleep(1 * time.Second)
	}

	// Provide detailed error message for debugging
	if !subsystemVerified {
		return "", fmt.Errorf("%w after %d attempts (subsystem %s never appeared in nvme list-subsys - kernel may not have registered the connection)", ErrNVMeDeviceTimeout, attempt, nqn)
	}
	return "", fmt.Errorf("%w after %d attempts (subsystem was connected but device path not found in sysfs)", ErrNVMeDeviceTimeout, attempt)
}

// isDeviceHealthy does a quick check if a device is functional (non-zero size).
// This is a single check, not a retry loop like verifyDeviceHealthy.
func (s *NodeService) isDeviceHealthy(ctx context.Context, devicePath string) bool {
	sizeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	sizeStr := strings.TrimSpace(string(output))
	size, parseErr := strconv.ParseInt(sizeStr, 10, 64)
	return parseErr == nil && size > 0
}

// handleDeviceFormatting checks if a device needs formatting and formats it if necessary.
func (s *NodeService) handleDeviceFormatting(ctx context.Context, volumeID, devicePath, fsType, datasetName, nqn string, isClone bool) error {
	// Check if device is already formatted
	needsFormat, err := needsFormatWithRetries(ctx, devicePath, isClone)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if needsFormat {
		klog.V(4).Infof("Device %s needs formatting with %s (dataset: %s)", devicePath, fsType, datasetName)
		if formatErr := formatDevice(ctx, volumeID, devicePath, fsType); formatErr != nil {
			return status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}
		return nil
	}

	klog.V(4).Infof("Device %s is already formatted, preserving existing filesystem (dataset: %s, NQN: %s)",
		devicePath, datasetName, nqn)
	return nil
}

// logDeviceInfo logs detailed information about an NVMe device for troubleshooting.
func (s *NodeService) logDeviceInfo(ctx context.Context, devicePath string) {
	// Log basic device info
	if stat, err := os.Stat(devicePath); err == nil {
		klog.V(4).Infof("Device %s: exists, size=%d bytes", devicePath, stat.Size())
	} else {
		klog.Warningf("Device %s: stat failed: %v", devicePath, err)
		return
	}

	// Get actual device size using blockdev
	sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sizeCancel()
	sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	if sizeOutput, err := sizeCmd.CombinedOutput(); err == nil {
		deviceSize := strings.TrimSpace(string(sizeOutput))
		klog.V(4).Infof("Device %s has size: %s bytes", devicePath, deviceSize)
	} else {
		klog.Warningf("Failed to get device size for %s: %v", devicePath, err)
	}

	// Try to get device UUID (for better tracking)
	uuidCtx, uuidCancel := context.WithTimeout(ctx, 3*time.Second)
	defer uuidCancel()
	blkidCmd := exec.CommandContext(uuidCtx, "blkid", "-s", "UUID", "-o", "value", devicePath)
	if uuidOutput, err := blkidCmd.CombinedOutput(); err == nil && len(uuidOutput) > 0 {
		uuid := strings.TrimSpace(string(uuidOutput))
		if uuid != "" {
			klog.V(4).Infof("Device %s has filesystem UUID: %s", devicePath, uuid)
		}
	}

	// Try to get filesystem type
	fsTypeCtx, fsTypeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer fsTypeCancel()
	fsCmd := exec.CommandContext(fsTypeCtx, "blkid", "-s", "TYPE", "-o", "value", devicePath)
	if fsOutput, err := fsCmd.CombinedOutput(); err == nil && len(fsOutput) > 0 {
		fsType := strings.TrimSpace(string(fsOutput))
		if fsType != "" {
			klog.V(4).Infof("Device %s has filesystem type: %s", devicePath, fsType)
		}
	}
}

// verifyDeviceSize compares the actual device size with expected capacity from volume context or TrueNAS API.
func (s *NodeService) verifyDeviceSize(ctx context.Context, devicePath string, volumeContext map[string]string) error {
	datasetName := volumeContext["datasetName"]

	// Get actual device size
	actualSize, err := getBlockDeviceSize(ctx, devicePath)
	if err != nil {
		return err
	}
	klog.V(4).Infof("Device %s (dataset: %s) actual size: %d bytes (%d GiB)", devicePath, datasetName, actualSize, actualSize/(1024*1024*1024))

	// Get expected capacity from volume context or TrueNAS API
	expectedCapacity := s.getExpectedCapacity(ctx, devicePath, datasetName, volumeContext)

	// If no expected capacity available, skip verification
	if expectedCapacity == 0 {
		klog.Warningf("No expectedCapacity available for device %s, skipping size verification", devicePath)
		return nil
	}

	// Verify the device size matches expected capacity
	return verifySizeMatch(devicePath, actualSize, expectedCapacity, datasetName, volumeContext)
}

// getBlockDeviceSize returns the size of a block device in bytes.
func getBlockDeviceSize(ctx context.Context, devicePath string) (int64, error) {
	sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sizeCancel()
	sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	sizeOutput, err := sizeCmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get device size: %w", err)
	}

	actualSize, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse device size: %w", err)
	}
	return actualSize, nil
}

// getExpectedCapacity retrieves the expected capacity from volumeContext or TrueNAS API.
func (s *NodeService) getExpectedCapacity(ctx context.Context, devicePath, datasetName string, volumeContext map[string]string) int64 {
	// Try volume context first
	if expectedCapacityStr := volumeContext["expectedCapacity"]; expectedCapacityStr != "" {
		if capacity, err := strconv.ParseInt(expectedCapacityStr, 10, 64); err == nil {
			return capacity
		}
		klog.Warningf("Failed to parse expectedCapacity '%s' for %s", expectedCapacityStr, devicePath)
	}

	// Query TrueNAS API if not in volumeContext
	if datasetName != "" && s.apiClient != nil {
		klog.V(4).Infof("Querying TrueNAS API for ZVOL size of %s", datasetName)
		dataset, err := s.apiClient.Dataset(ctx, datasetName)
		if err != nil {
			klog.Warningf("Failed to query ZVOL size from TrueNAS API for %s: %v", datasetName, err)
			return 0
		}
		if dataset != nil && dataset.Volsize != nil {
			if parsedSize, ok := dataset.Volsize["parsed"].(float64); ok {
				klog.V(4).Infof("Got expected capacity %d bytes from TrueNAS API for %s", int64(parsedSize), devicePath)
				return int64(parsedSize)
			}
		}
	}
	return 0
}

// verifySizeMatch compares actual and expected sizes.
// Device being LARGER than expected is allowed (volume expansion case).
// Device being SMALLER than expected by more than tolerance is an error (wrong device).
func verifySizeMatch(devicePath string, actualSize, expectedCapacity int64, datasetName string, volumeContext map[string]string) error {
	// If device is larger than expected, that's fine (volume was expanded)
	if actualSize >= expectedCapacity {
		klog.V(4).Infof("Device size verification passed for %s: expected=%d, actual=%d (device is same or larger)",
			devicePath, expectedCapacity, actualSize)
		return nil
	}

	// Device is smaller than expected - check if within tolerance
	sizeDiff := expectedCapacity - actualSize

	// Calculate tolerance: 10% of expected capacity, minimum 100MB
	tolerance := expectedCapacity / 10
	const minTolerance = 100 * 1024 * 1024
	if tolerance < minTolerance {
		tolerance = minTolerance
	}

	if sizeDiff > tolerance {
		klog.Errorf("CRITICAL: Device size mismatch detected for %s!", devicePath)
		klog.Errorf("  Expected capacity: %d bytes (%d GiB)", expectedCapacity, expectedCapacity/(1024*1024*1024))
		klog.Errorf("  Actual device size: %d bytes (%d GiB)", actualSize, actualSize/(1024*1024*1024))
		klog.Errorf("  Difference: %d bytes (%d GiB)", sizeDiff, sizeDiff/(1024*1024*1024))
		klog.Errorf("  Dataset: %s, NQN: %s", datasetName, volumeContext["nqn"])
		return fmt.Errorf("%w: expected %d bytes, got %d bytes (diff: %d bytes)",
			ErrDeviceSizeMismatch, expectedCapacity, actualSize, sizeDiff)
	}

	klog.V(4).Infof("Device size verification passed for %s: expected=%d, actual=%d, diff=%d (within tolerance=%d)",
		devicePath, expectedCapacity, actualSize, sizeDiff, tolerance)
	return nil
}

// forceDeviceRescan forces the kernel to completely re-read device identity and metadata.
func forceDeviceRescan(ctx context.Context, devicePath string) error {
	klog.V(4).Infof("Forcing device rescan for %s to clear kernel caches", devicePath)

	// Step 1: Sync and flush device buffers
	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Second)
	defer syncCancel()
	syncCmd := exec.CommandContext(syncCtx, "sync")
	if output, err := syncCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("sync command failed: %v, output: %s", err, string(output))
	}

	// Step 2: Flush device buffers
	flushCtx, flushCancel := context.WithTimeout(ctx, 5*time.Second)
	defer flushCancel()
	flushCmd := exec.CommandContext(flushCtx, "blockdev", "--flushbufs", devicePath)
	if output, err := flushCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("blockdev --flushbufs failed for %s: %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Flushed device buffers for %s", devicePath)
	}

	// Step 3: Trigger udev to re-process the device
	udevCtx, udevCancel := context.WithTimeout(ctx, 5*time.Second)
	defer udevCancel()
	udevCmd := exec.CommandContext(udevCtx, "udevadm", "trigger", "--action=change", devicePath)
	if output, err := udevCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger failed for %s: %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev change event for %s", devicePath)
	}

	// Step 4: Wait for udev to settle
	settleCtx, settleCancel := context.WithTimeout(ctx, 10*time.Second)
	defer settleCancel()
	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=5")
	if output, err := settleCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm settle failed: %v, output: %s", err, string(output))
	} else {
		klog.V(4).Infof("udevadm settle completed")
	}

	klog.V(4).Infof("Device rescan completed for %s", devicePath)
	return nil
}

// rescanNVMeNamespace rescans an NVMe namespace to ensure the kernel has fresh device data.
func (s *NodeService) rescanNVMeNamespace(ctx context.Context, devicePath string) error {
	// Extract controller path from device path (e.g., /dev/nvme0n1 -> /dev/nvme0)
	controllerPath := extractNVMeController(devicePath)
	if controllerPath == "" {
		return fmt.Errorf("%w: %s", ErrNVMeControllerNotFound, devicePath)
	}

	klog.V(4).Infof("Rescanning NVMe namespace on controller %s (device: %s)", controllerPath, devicePath)

	rescanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	//nolint:gosec // nvme ns-rescan with controller path derived from device path is expected for CSI driver
	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme ns-rescan failed for %s: %v, output: %s (this may be OK)", controllerPath, err, string(output))
		return fmt.Errorf("ns-rescan failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully rescanned NVMe namespace on controller %s", controllerPath)
	return nil
}

// extractNVMeController extracts the controller device path from a namespace device path
// (e.g., /dev/nvme0n1 -> /dev/nvme0, /dev/nvme1n2 -> /dev/nvme1).
func extractNVMeController(devicePath string) string {
	// Find the position of 'n' followed by a digit (the namespace part)
	for i := len(devicePath) - 1; i >= 0; i-- {
		if devicePath[i] == 'n' && i > 0 && devicePath[i-1] >= '0' && devicePath[i-1] <= '9' {
			if i+1 < len(devicePath) && devicePath[i+1] >= '0' && devicePath[i+1] <= '9' {
				return devicePath[:i]
			}
		}
	}
	return ""
}

// disconnectNVMeOF disconnects from an NVMe-oF target and waits for device cleanup.
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

	// Wait for kernel to cleanup device nodes
	const deviceCleanupDelay = 1 * time.Second
	klog.V(4).Infof("Waiting %v for kernel to cleanup NVMe devices after disconnect", deviceCleanupDelay)
	select {
	case <-time.After(deviceCleanupDelay):
		klog.V(4).Infof("Device cleanup delay complete")
	case <-ctx.Done():
		klog.Warningf("Context canceled during device cleanup delay: %v", ctx.Err())
		return ctx.Err()
	}

	return nil
}

// getNVMeOFMountOptions merges user-provided mount options with sensible defaults.
// User options take precedence - if a user specifies an option that conflicts
// with a default, the user's option wins.
// This allows StorageClass mountOptions to fully customize NVMe-oF filesystem mount behavior.
func getNVMeOFMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultNVMeOFMountOptions
	}

	// Build a map of option keys that the user has specified
	// This handles both key=value options and flags (e.g., "noatime", "ro")
	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractNVMeOFOptionKey(opt)
		userOptionKeys[key] = true
	}

	// Start with user options, then add defaults that don't conflict
	result := make([]string, 0, len(userOptions)+len(defaultNVMeOFMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultNVMeOFMountOptions {
		key := extractNVMeOFOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}

// extractNVMeOFOptionKey extracts the key from a mount option.
// For "key=value" options, returns "key".
// For flag options like "noatime" or "ro", returns the flag itself.
func extractNVMeOFOptionKey(option string) string {
	for i, c := range option {
		if c == '=' {
			return option[:i]
		}
	}
	return option
}
