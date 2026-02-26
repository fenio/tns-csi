package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fenio/tns-csi/pkg/retry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// connectNVMeOFTarget discovers and connects to an NVMe-oF target with retry logic.
// This handles transient failures when TrueNAS has just created a new subsystem
// (e.g., for snapshot-restored volumes) but it's not yet fully ready for connections.
func (s *NodeService) connectNVMeOFTarget(ctx context.Context, params *nvmeOFConnectionParams) error {
	if s.enableDiscovery {
		// Discover the NVMe-oF target
		klog.V(4).Infof("Discovering NVMe-oF target at %s:%s", params.server, params.port)
		discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
		defer discoverCancel()
		discoverCmd := exec.CommandContext(discoverCtx, "nvme", "discover", "-t", params.transport, "-a", params.server, "-s", params.port)
		if output, discoverErr := discoverCmd.CombinedOutput(); discoverErr != nil {
			klog.Warningf("NVMe discover failed (this may be OK if target is already known): %v, output: %s", discoverErr, string(output))
		}
	} else {
		klog.V(4).Infof("Skipping NVMe discover for %s (all connection params known from volume context)", params.nqn)
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

	// After successful connection, give the kernel time to register the controller
	// and enumerate namespaces. This initial delay helps prevent the race condition
	// where we look for the device before the kernel has finished setting it up.
	const postConnectDelay = 2 * time.Second
	klog.V(4).Infof("Waiting %v for kernel to register NVMe controller and namespaces", postConnectDelay)
	time.Sleep(postConnectDelay)

	// Trigger udev to process new NVMe devices
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
	// --nr-io-queues: Number of I/O queues (default 4; configurable via StorageClass)
	// --queue-size: Queue depth per I/O queue (kernel default 127; configurable via StorageClass)
	connectArgs := []string{
		"connect",
		"-t", params.transport,
		"-n", params.nqn,
		"-a", params.server,
		"-s", params.port,
		"--reconnect-delay=2",
		"--ctrl-loss-tmo=60",
		"--keep-alive-tmo=5",
	}

	if params.nrIOQueues != "" {
		connectArgs = append(connectArgs, "--nr-io-queues="+params.nrIOQueues)
		klog.V(4).Infof("Using custom nr-io-queues=%s for NVMe-oF connection", params.nrIOQueues)
	} else {
		connectArgs = append(connectArgs, "--nr-io-queues=4") // default
	}

	if params.queueSize != "" {
		connectArgs = append(connectArgs, "--queue-size="+params.queueSize)
		klog.V(4).Infof("Using custom queue-size=%s for NVMe-oF connection", params.queueSize)
	}

	connectCmd := exec.CommandContext(connectCtx, "nvme", connectArgs...)
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
		// Check if device disappeared (common during cleanup race conditions)
		if _, statErr := os.Stat(devicePath); statErr != nil {
			return status.Errorf(codes.Unavailable, "device %s became unavailable: %v", devicePath, err)
		}
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
