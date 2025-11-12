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

// Static errors and constants for device operations.
var (
	// ErrUnsupportedFSType is returned when attempting to format a device with an unsupported filesystem type.
	ErrUnsupportedFSType = errors.New("unsupported filesystem type")
	// ErrDeviceNotReady is returned when a device does not become ready after retries.
	ErrDeviceNotReady = errors.New("device not ready after retries")
)

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
	mounted, err := mount.IsDeviceMounted(ctx, targetPath)
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

	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

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

// publishFilesystemVolume publishes a filesystem volume by bind mounting the staged directory to target.
func (s *NodeService) publishFilesystemVolume(ctx context.Context, stagingTargetPath, targetPath string, readonly bool) (*csi.NodePublishVolumeResponse, error) {
	klog.Infof("Publishing filesystem volume from %s to %s", stagingTargetPath, targetPath)

	// Verify staging path exists and is a directory
	stagingInfo, err := os.Stat(stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Staging path %s not found: %v", stagingTargetPath, err)
	}

	// For filesystem volumes, staging path should be a directory
	if !stagingInfo.IsDir() {
		return nil, status.Errorf(codes.Internal, "Staging path %s is not a directory", stagingTargetPath)
	}

	// Create target directory if it doesn't exist
	err = os.MkdirAll(targetPath, 0o750)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create target directory %s: %v", targetPath, err)
	}

	// Check if already mounted
	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if target path is mounted: %v", err)
	}
	if mounted {
		klog.V(4).Infof("Target path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Bind mount the staged directory to target
	mountOptions := []string{"bind"}
	if readonly {
		mountOptions = append(mountOptions, "ro")
	}

	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to bind mount filesystem: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully bind mounted filesystem to %s", targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
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

// invalidateDeviceCache invalidates kernel caches for a device.
// This is critical for cloned ZVOLs where the kernel may cache the "empty" state
// before the clone completes, preventing blkid from detecting the existing filesystem.
func invalidateDeviceCache(ctx context.Context, devicePath string, attempt int) error {
	// Only run cache invalidation on retry attempts (not first attempt)
	if attempt == 0 {
		return nil
	}

	// Use blockdev --flushbufs to invalidate kernel buffer cache
	// This forces the kernel to re-read the device's actual content
	flushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(flushCtx, "blockdev", "--flushbufs", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("blockdev --flushbufs failed for %s (attempt %d): %v, output: %s",
			devicePath, attempt+1, err, string(output))
		// Don't fail - device might not exist yet, continue anyway
		return err
	}
	klog.V(4).Infof("Flushed device buffers for %s (attempt %d)", devicePath, attempt+1)

	// Wait for udev to settle (process any pending device events)
	// This ensures udev has processed any changes to the device
	settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
	defer cancelSettle()

	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=5")
	settleOutput, settleErr := settleCmd.CombinedOutput()
	if settleErr != nil {
		klog.V(4).Infof("udevadm settle failed (attempt %d): %v, output: %s",
			attempt+1, settleErr, string(settleOutput))
		return settleErr
	}
	klog.V(4).Infof("udevadm settle completed (attempt %d)", attempt+1)

	return nil
}

// needsFormat checks if a device needs to be formatted.
// For block devices (especially cloned ZVOLs and reconnected NVMe-oF devices),
// retry with exponential backoff to allow the device to become fully ready
// before checking for existing filesystem.
//
// CRITICAL: This function must be extremely conservative about declaring a device
// "needs formatting" because formatting destroys all existing data. After NVMe-oF
// reconnections (especially after forced pod deletion), filesystem metadata may
// take time to become visible to blkid.
func needsFormat(ctx context.Context, devicePath string) (bool, error) {
	const (
		maxRetries     = 15
		initialBackoff = 200 * time.Millisecond
		maxBackoff     = 5 * time.Second
	)

	klog.Infof("Checking if device %s needs formatting (max %d retries with up to %v backoff - being conservative to prevent data loss)",
		devicePath, maxRetries, maxBackoff)

	// CRITICAL: For NVMe devices, add initial stabilization delay before first check
	if err := waitForNVMeStabilization(ctx, devicePath); err != nil {
		return false, err
	}

	// Retry with exponential backoff to handle device readiness timing
	lastOutput, lastErr := retryFilesystemCheck(ctx, devicePath, maxRetries, initialBackoff, maxBackoff)

	// After all retries, handle the final result
	return handleFinalResult(devicePath, maxRetries, lastOutput, lastErr)
}

// waitForNVMeStabilization adds stabilization delay for NVMe devices.
// This delay is in addition to the delay in stageNVMeDevice and provides
// extra protection before the filesystem check retry loop begins.
func waitForNVMeStabilization(ctx context.Context, devicePath string) error {
	if !strings.Contains(devicePath, "/dev/nvme") {
		return nil
	}

	const nvmeInitialDelay = 2 * time.Second
	klog.V(4).Infof("NVMe device detected, waiting %v before first filesystem check", nvmeInitialDelay)
	select {
	case <-time.After(nvmeInitialDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryFilesystemCheck performs filesystem checks with exponential backoff.
func retryFilesystemCheck(ctx context.Context, devicePath string, maxRetries int, initialBackoff, maxBackoff time.Duration) ([]byte, error) {
	var lastErr error
	var lastOutput []byte
	backoff := initialBackoff

	for attempt := range maxRetries {
		if attempt > 0 {
			if err := waitWithBackoff(ctx, devicePath, attempt, maxRetries, backoff); err != nil {
				return lastOutput, err
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		// Invalidate kernel caches before checking filesystem
		if err := invalidateDeviceCache(ctx, devicePath, attempt); err != nil {
			klog.Warningf("Failed to invalidate device cache for %s (attempt %d): %v - continuing anyway", devicePath, attempt+1, err)
		}

		// Check device filesystem status and handle result
		needsFmt, output, err := checkDeviceFilesystem(ctx, devicePath)
		lastOutput = output
		lastErr = err

		if shouldStopRetrying(needsFmt, err, devicePath, attempt, maxRetries, output) {
			return lastOutput, lastErr
		}
	}

	return lastOutput, lastErr
}

// shouldStopRetrying determines if we should stop retrying filesystem checks.
func shouldStopRetrying(needsFmt bool, err error, devicePath string, attempt, maxRetries int, output []byte) bool {
	// Log detailed information about this attempt
	if needsFmt || err != nil {
		klog.Warningf("needsFormat attempt %d/%d for %s: needsFormat=%v, err=%v, output=%q (will retry if uncertain)",
			attempt+1, maxRetries, devicePath, needsFmt, err, string(output))
	} else {
		klog.Infof("needsFormat attempt %d/%d for %s: filesystem detected, needsFormat=false",
			attempt+1, maxRetries, devicePath)
	}

	// CRITICAL: For NVMe devices with no filesystem detected, continue retrying
	// This handles the case where a volume was cloned from a snapshot - the filesystem
	// exists but may not be visible yet due to ZFS metadata sync timing.
	// We must retry to avoid destroying cloned data by reformatting too early.
	if err == nil && needsFmt && strings.Contains(devicePath, "/dev/nvme") {
		if attempt+1 < maxRetries {
			klog.Infof("NVMe device %s has no filesystem detected yet (attempt %d/%d) - will retry to avoid destroying potential clone data",
				devicePath, attempt+1, maxRetries)
			return false // Continue retrying
		}
		// Reached max retries - stop and proceed to format
		klog.Warningf("Device %s appears to need formatting after %d attempts - will fail unless force-format annotation is set",
			devicePath, attempt+1)
		return true
	}

	// If filesystem check succeeded with a definitive result (filesystem found or max retries reached)
	if err == nil {
		if needsFmt {
			klog.Warningf("Device %s appears to need formatting (no filesystem detected after %d attempts) - will fail unless force-format annotation is set",
				devicePath, attempt+1)
		} else {
			klog.Infof("Device %s has existing filesystem, skipping format (detected after %d attempts)", devicePath, attempt+1)
		}
		return true
	}

	// If device doesn't exist yet, continue retrying
	if isDeviceNotReady(output) {
		klog.Infof("Device %s not ready yet (attempt %d/%d): %s - will retry", devicePath, attempt+1, maxRetries, string(output))
		return false
	}

	// For other errors, continue retrying
	klog.Infof("blkid returned error for %s (attempt %d/%d): %v, output: %q - will retry",
		devicePath, attempt+1, maxRetries, err, string(output))
	return false
}

// waitWithBackoff waits for the specified backoff duration before retry.
func waitWithBackoff(ctx context.Context, devicePath string, attempt, maxRetries int, backoff time.Duration) error {
	klog.V(4).Infof("Retrying blkid for %s (attempt %d/%d after %v)", devicePath, attempt+1, maxRetries, backoff)
	select {
	case <-time.After(backoff):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkDeviceFilesystem checks if a device has a filesystem using blkid and lsblk.
// Returns (needsFormat, output, error).
func checkDeviceFilesystem(ctx context.Context, devicePath string) (needsFormat bool, output []byte, err error) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// First, check with lsblk to verify device exists and get basic info
	lsblkCmd := exec.CommandContext(checkCtx, "lsblk", "-n", "-o", "FSTYPE", devicePath)
	lsblkOutput, lsblkErr := lsblkCmd.CombinedOutput()

	if lsblkErr != nil {
		// Device doesn't exist or lsblk failed
		klog.V(4).Infof("lsblk failed for %s: %v, output: %s", devicePath, lsblkErr, string(lsblkOutput))
		return false, lsblkOutput, lsblkErr
	}

	// lsblk succeeded - check if FSTYPE is empty (no filesystem)
	fstype := strings.TrimSpace(string(lsblkOutput))
	if fstype == "" {
		klog.V(4).Infof("lsblk shows device %s has no filesystem (FSTYPE empty)", devicePath)
		// Verify with blkid for consistency
		blkidCmd := exec.CommandContext(checkCtx, "blkid", devicePath)
		blkidOutput, blkidErr := blkidCmd.CombinedOutput()

		if blkidErr != nil || len(blkidOutput) == 0 || strings.Contains(string(blkidOutput), "does not contain") {
			klog.Infof("Device %s confirmed to have no filesystem (lsblk FSTYPE='', blkid confirms)", devicePath)
			// Return empty output to indicate no filesystem detected (handleFinalResult expects this)
			return true, nil, nil
		}

		// Conflicting information - blkid found filesystem but lsblk didn't
		klog.Warningf("Device %s: lsblk shows no FSTYPE but blkid found filesystem: %s - trusting blkid",
			devicePath, string(blkidOutput))
		return false, blkidOutput, nil
	}

	// lsblk shows a filesystem type - verify with blkid
	klog.V(4).Infof("lsblk shows device %s has filesystem type: %s", devicePath, fstype)

	blkidCmd := exec.CommandContext(checkCtx, "blkid", devicePath)
	blkidOutput, blkidErr := blkidCmd.CombinedOutput()

	if blkidErr == nil {
		klog.V(4).Infof("Device %s has existing filesystem confirmed by both lsblk and blkid: %s",
			devicePath, string(blkidOutput))
		return false, blkidOutput, nil
	}

	// lsblk found filesystem but blkid didn't - trust lsblk
	klog.V(4).Infof("Device %s: lsblk found FSTYPE=%s, blkid failed - trusting lsblk", devicePath, fstype)
	return false, lsblkOutput, nil
}

// isDeviceNotReady checks if blkid output indicates device is not ready.
func isDeviceNotReady(output []byte) bool {
	return strings.Contains(string(output), "No such device") || strings.Contains(string(output), "No such file")
}

// handleFinalResult processes the final result after all retries.
func handleFinalResult(devicePath string, maxRetries int, lastOutput []byte, lastErr error) (bool, error) {
	// If blkid check succeeded (lastErr == nil), we need to determine if filesystem was detected
	// based on the output. Empty output or "does not contain" means no filesystem detected.
	if lastErr == nil {
		// Check if no filesystem was detected
		if len(lastOutput) == 0 || strings.Contains(string(lastOutput), "does not contain") {
			klog.Infof("Device %s has no filesystem - needs formatting", devicePath)
			return true, nil
		}
		// Filesystem was detected, no formatting needed
		klog.V(4).Infof("Device %s has existing filesystem, skipping format", devicePath)
		return false, nil
	}

	// After all retries, if blkid failed but output suggests no filesystem, device needs formatting.
	// This is standard CSI behavior - new volumes should be formatted automatically.
	// The extensive retry logic (15 attempts with cache invalidation) protects against
	// temporary detection issues during device reconnection/clone completion.
	if len(lastOutput) == 0 || strings.Contains(string(lastOutput), "does not contain") {
		klog.Infof("Device %s has no filesystem after %d retries - needs formatting", devicePath, maxRetries)
		return true, nil
	}

	// Device still not ready - this is unexpected
	return false, fmt.Errorf("%w: device %s not ready after %d retries: %w (output: %s)",
		ErrDeviceNotReady, devicePath, maxRetries, lastErr, string(lastOutput))
}

// formatDevice formats a device with the specified filesystem.
// This function performs the actual formatting operation. The caller is responsible
// for determining whether formatting is appropriate (e.g., checking needsFormat first).
func formatDevice(ctx context.Context, volumeID, devicePath, fsType string) error {
	klog.Infof("Formatting volume %s at %s with filesystem %s", volumeID, devicePath, fsType)

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
	klog.Infof("Successfully formatted volume %s at %s with filesystem %s", volumeID, devicePath, fsType)

	return nil
}
