package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for NVMe-oF operations.
var (
	ErrNVMeCLINotFound             = errors.New("nvme command not found - please install nvme-cli")
	ErrNVMeDeviceNotFound          = errors.New("NVMe device not found")
	ErrNVMeDeviceUnhealthy         = errors.New("NVMe device exists but is unhealthy (zero size)")
	ErrNVMeDeviceTimeout           = errors.New("timeout waiting for NVMe device to appear")
	ErrNVMeSubsystemTimeout        = errors.New("timeout waiting for NVMe subsystem to become live")
	ErrDeviceInitializationTimeout = errors.New("device failed to initialize - size remained zero or unreadable")
	ErrNVMeControllerNotFound      = errors.New("could not extract NVMe controller path from device path")
	ErrDeviceSizeMismatch          = errors.New("device size does not match expected capacity")
)

// NVMe subsystem states.
const (
	nvmeSubsystemStateLive = "live"
)

// defaultNVMeOFMountOptions are sensible defaults for NVMe-oF filesystem mounts.
// These are merged with user-specified mount options from StorageClass.
var defaultNVMeOFMountOptions = []string{"noatime"}

// nvmeOFConnectionParams holds validated NVMe-oF connection parameters.
// With independent subsystems per volume, NSID is always 1.
type nvmeOFConnectionParams struct {
	nqn        string
	server     string
	transport  string
	port       string
	nrIOQueues string // optional: --nr-io-queues flag value
	queueSize  string // optional: --queue-size flag value
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

	// Acquire semaphore to limit concurrent NVMe-oF connect operations.
	// This prevents overwhelming the kernel's NVMe subsystem registration lock
	// when many volumes are being staged simultaneously.
	klog.V(4).Infof("Waiting for NVMe-oF connect semaphore (capacity: %d) for NQN: %s", cap(s.nvmeConnectSem), params.nqn)
	metrics.NVMeConnectWaiting()
	select {
	case s.nvmeConnectSem <- struct{}{}:
		metrics.NVMeConnectDoneWaiting()
		metrics.NVMeConnectStart()
		defer func() {
			<-s.nvmeConnectSem
			metrics.NVMeConnectDone()
		}()
	case <-ctx.Done():
		metrics.NVMeConnectDoneWaiting()
		return nil, status.Errorf(codes.DeadlineExceeded,
			"timed out waiting for NVMe-oF connect semaphore (max concurrent: %d): %v",
			cap(s.nvmeConnectSem), ctx.Err())
	}
	klog.V(4).Infof("Acquired NVMe-oF connect semaphore for NQN: %s", params.nqn)

	// Connect to NVMe-oF target and stage device
	return s.connectAndStageDevice(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext, datasetName)
}

// tryReuseExistingConnection attempts to reuse an existing NVMe-oF connection.
// Returns the response if successful, or nil if no existing connection found.
// With independent subsystems, we simply check if the device for this NQN exists.
func (s *NodeService) tryReuseExistingConnection(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (resp *csi.NodeStageVolumeResponse, devicePath string, err error) {
	// With independent subsystems, NSID is always 1
	devicePath, findErr := s.findNVMeDeviceByNQN(ctx, params.nqn)

	// Check if we found an unhealthy device (stale connection from previous run)
	// This is different from "not found" - we need to disconnect it before reconnecting
	if errors.Is(findErr, ErrNVMeDeviceUnhealthy) {
		klog.Warningf("Found stale NVMe connection for NQN %s (unhealthy device) - disconnecting before reconnect", params.nqn)
		if disconnectErr := s.disconnectNVMeOF(ctx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect stale NVMe-oF connection: %v", disconnectErr)
		}
		// Wait for cleanup
		time.Sleep(2 * time.Second)
		return nil, "", nil
	}

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
// Uses aggressive retry logic similar to democratic-csi to handle transient failures:
// 1. Connect to target
// 2. Wait for subsystem state to become "live" (blocking)
// 3. Wait for device path to appear
// 4. Retry entire cycle if any step fails.
//
// IMPORTANT: This function uses its own internal timeouts rather than the parent context
// for retry operations. This prevents the CSI sidecar's context deadline from causing
// cascading failures in our retry loop. The parent context is only checked at the start
// of each attempt to allow graceful termination.
func (s *NodeService) connectAndStageDevice(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string, datasetName string) (*csi.NodeStageVolumeResponse, error) {
	const (
		stateWaitTimeout  = 60 * time.Second // Wait for subsystem to become "live"
		deviceWaitTimeout = 60 * time.Second // Wait for device path to appear
		maxConnectRetries = 10               // Try up to 10 connect cycles
		retryDelay        = 2 * time.Second  // Delay between retries
	)

	var lastErr error
	for attempt := 1; attempt <= maxConnectRetries; attempt++ {
		// Check if parent context is canceled before starting new attempt.
		// This allows graceful termination while not letting context cancellation
		// cascade into our internal operations.
		select {
		case <-ctx.Done():
			klog.Warningf("Parent context canceled, stopping NVMe-oF connection attempts at attempt %d: %v", attempt, ctx.Err())
			if lastErr != nil {
				return nil, status.Errorf(codes.DeadlineExceeded, "NVMe-oF connection canceled after %d attempts (last error: %v)", attempt-1, lastErr)
			}
			return nil, status.Errorf(codes.DeadlineExceeded, "NVMe-oF connection canceled: %v", ctx.Err())
		default:
		}

		if attempt > 1 {
			klog.Infof("Retrying NVMe-oF connection (attempt %d/%d) for NQN: %s", attempt, maxConnectRetries, params.nqn)
		}

		// Use a detached context for internal operations to prevent the CSI sidecar's
		// context deadline from causing cascading failures in our retry loop.
		// Each operation (connect, state wait, device wait) has its own timeout.
		// This is intentional - we check ctx.Done() at the start of each attempt instead.
		opCtx := context.Background()

		// Step 1: Connect to NVMe-oF target
		//nolint:contextcheck // Intentionally using detached context - see comment above
		if connectErr := s.connectNVMeOFTarget(opCtx, params); connectErr != nil {
			lastErr = connectErr
			klog.Warningf("NVMe-oF connect attempt %d failed: %v", attempt, connectErr)
			if attempt < maxConnectRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		// Step 2: Wait for subsystem to become "live" (critical for reliability)
		// This is what democratic-csi does - it blocks until state == "live" before looking for devices
		klog.V(4).Infof("Waiting for subsystem %s to become live...", params.nqn)
		//nolint:contextcheck // Intentionally using detached context - see comment above
		if stateErr := waitForSubsystemLive(opCtx, params.nqn, stateWaitTimeout); stateErr != nil {
			lastErr = stateErr
			klog.Warningf("NVMe-oF subsystem %s did not become live on attempt %d: %v", params.nqn, attempt, stateErr)

			// Disconnect before retry
			//nolint:contextcheck // Intentionally using detached context - see comment above
			if disconnectErr := s.disconnectNVMeOF(opCtx, params.nqn); disconnectErr != nil {
				klog.Warningf("Failed to disconnect after subsystem state timeout: %v", disconnectErr)
			}

			if attempt < maxConnectRetries {
				klog.V(4).Infof("Waiting %v before retry...", retryDelay)
				time.Sleep(retryDelay)
			}
			continue
		}

		// Step 3: Wait for device path to appear (NSID is always 1 with independent subsystems)
		//nolint:contextcheck // Intentionally using detached context - see comment above
		devicePath, err := s.waitForNVMeDevice(opCtx, params.nqn, deviceWaitTimeout)
		if err == nil {
			klog.Infof("NVMe-oF device connected at %s (NQN: %s, dataset: %s) on attempt %d",
				devicePath, params.nqn, datasetName, attempt)

			// Try staging - if device becomes unavailable during staging, retry the whole connection
			// Use original context for staging since that's the actual CSI operation
			stageResp, stageErr := s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
			if stageErr == nil {
				return stageResp, nil
			}

			// Check if this is a retryable error (device disappeared during staging)
			if status.Code(stageErr) == codes.Unavailable {
				lastErr = stageErr
				klog.Warningf("NVMe-oF staging failed on attempt %d (device unstable): %v", attempt, stageErr)
				// Disconnect and retry - the device may have become stale
				//nolint:contextcheck // Intentionally using detached context
				if disconnectErr := s.disconnectNVMeOF(opCtx, params.nqn); disconnectErr != nil {
					klog.Warningf("Failed to disconnect after staging failure: %v", disconnectErr)
				}
				if attempt < maxConnectRetries {
					time.Sleep(retryDelay)
				}
				continue
			}

			// Non-retryable error - fail immediately
			return nil, stageErr
		}

		lastErr = err
		klog.Warningf("NVMe-oF device wait failed on attempt %d: %v", attempt, err)

		// Disconnect before retry (or final cleanup)
		//nolint:contextcheck // Intentionally using detached context - see comment above
		if disconnectErr := s.disconnectNVMeOF(opCtx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect NVMe-oF after device wait failure: %v", disconnectErr)
		}

		// Delay before retry to let things settle
		if attempt < maxConnectRetries {
			klog.V(4).Infof("Waiting %v before retry...", retryDelay)
			time.Sleep(retryDelay)
		}
	}

	return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after %d connection attempts (NQN: %s): %v",
		maxConnectRetries, params.nqn, lastErr)
}

// validateNVMeOFParams validates and extracts NVMe-oF connection parameters from volume context.
// With independent subsystems, nsid is not required (always 1).
func (s *NodeService) validateNVMeOFParams(volumeContext map[string]string) (*nvmeOFConnectionParams, error) {
	params := &nvmeOFConnectionParams{
		nqn:        volumeContext["nqn"],
		server:     volumeContext["server"],
		transport:  volumeContext["transport"],
		port:       volumeContext["port"],
		nrIOQueues: volumeContext["nvmeof.nr-io-queues"],
		queueSize:  volumeContext["nvmeof.queue-size"],
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

	// Verify device still exists before proceeding (it may have disappeared due to race conditions
	// with previous volume cleanup or controller reconnection)
	if _, err := os.Stat(devicePath); err != nil {
		klog.Warningf("NVMe device %s disappeared before staging could complete: %v", devicePath, err)
		return nil, status.Errorf(codes.Unavailable,
			"NVMe device %s became unavailable: %v", devicePath, err)
	}

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

	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Mounted NVMe device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
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
