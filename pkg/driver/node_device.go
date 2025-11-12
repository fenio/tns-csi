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

// Static errors for device operations.
var (
	ErrUnsupportedFSType      = errors.New("unsupported filesystem type")
	ErrDeviceNotReady         = errors.New("device not ready after retries")
	ErrVolumeAlreadyFormatted = errors.New("volume was previously formatted")
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

	var lastErr error
	var lastOutput []byte
	backoff := initialBackoff

	klog.Infof("Checking if device %s needs formatting (max %d retries with up to %v backoff - being conservative to prevent data loss)",
		devicePath, maxRetries, maxBackoff)

	// CRITICAL: For NVMe devices, add initial stabilization delay before first check
	// This is especially important for reconnected NVMe-oF devices where the kernel
	// may still be processing the device attachment even after the device appears
	if strings.Contains(devicePath, "/dev/nvme") {
		const nvmeInitialDelay = 1 * time.Second
		klog.V(4).Infof("NVMe device detected, waiting %v before first filesystem check", nvmeInitialDelay)
		select {
		case <-time.After(nvmeInitialDelay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	// Retry with exponential backoff to handle device readiness timing
	for attempt := range maxRetries {
		if attempt > 0 {
			if err := waitWithBackoff(ctx, devicePath, attempt, maxRetries, backoff); err != nil {
				return false, err
			}
			// Exponential backoff: double the wait time, up to maxBackoff
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		// Invalidate kernel caches before checking filesystem
		// This is critical for cloned ZVOLs and reconnected NVMe-oF devices
		// where filesystem metadata may be cached
		if err := invalidateDeviceCache(ctx, devicePath, attempt); err != nil {
			klog.Warningf("Failed to invalidate device cache for %s (attempt %d): %v - continuing anyway", devicePath, attempt+1, err)
		}

		// Check device filesystem status
		needsFmt, output, err := checkDeviceFilesystem(ctx, devicePath)
		lastOutput = output
		lastErr = err

		// Log detailed information about this attempt with WARNING level for visibility
		if needsFmt || err != nil {
			klog.Warningf("needsFormat attempt %d/%d for %s: needsFormat=%v, err=%v, output=%q (will retry if uncertain)",
				attempt+1, maxRetries, devicePath, needsFmt, err, string(output))
		} else {
			klog.Infof("needsFormat attempt %d/%d for %s: filesystem detected, needsFormat=false",
				attempt+1, maxRetries, devicePath)
		}

		if err == nil {
			if needsFmt {
				klog.Warningf("Device %s appears to need formatting (no filesystem detected after %d attempts) - format registry will make final decision",
					devicePath, attempt+1)
			} else {
				klog.Infof("Device %s has existing filesystem, skipping format (detected after %d attempts)", devicePath, attempt+1)
			}
			return needsFmt, nil
		}

		// If device doesn't exist yet, retry (might be a cloned ZVOL still being created)
		if isDeviceNotReady(output) {
			klog.Infof("Device %s not ready yet (attempt %d/%d): %s - will retry", devicePath, attempt+1, maxRetries, string(output))
			continue
		}

		// For other errors, retry in case of transient issues
		klog.Infof("blkid returned error for %s (attempt %d/%d): %v, output: %q - will retry",
			devicePath, attempt+1, maxRetries, err, string(output))
	}

	// After all retries, handle the final result
	return handleFinalResult(devicePath, maxRetries, lastOutput, lastErr)
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

// checkDeviceFilesystem checks if a device has a filesystem using blkid.
// Returns (needsFormat, output, error).
func checkDeviceFilesystem(ctx context.Context, devicePath string) (needsFormat bool, output []byte, err error) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "blkid", devicePath)
	output, err = cmd.CombinedOutput()

	// If blkid succeeds, device has a filesystem
	if err == nil {
		klog.V(4).Infof("Device %s has existing filesystem: %s", devicePath, string(output))
		return false, output, nil
	}

	// If blkid fails because no filesystem found, device needs formatting
	if len(output) == 0 || strings.Contains(string(output), "does not contain") {
		klog.V(4).Infof("Device %s has no filesystem, needs formatting", devicePath)
		return true, output, nil
	}

	// Return error for further handling
	return false, output, err
}

// isDeviceNotReady checks if blkid output indicates device is not ready.
func isDeviceNotReady(output []byte) bool {
	return strings.Contains(string(output), "No such device") || strings.Contains(string(output), "No such file")
}

// handleFinalResult processes the final result after all retries.
func handleFinalResult(devicePath string, maxRetries int, lastOutput []byte, lastErr error) (bool, error) {
	if lastErr == nil {
		return false, nil
	}

	// CRITICAL DATA LOSS PREVENTION:
	// If we still can't detect a filesystem after all retries, this could be:
	// 1. A genuinely new volume that needs formatting
	// 2. An existing volume with data, but we're having detection issues
	//
	// The SAFE approach: If blkid shows "no filesystem" after all retries,
	// assume it needs formatting. However, we should NEVER format a volume
	// that we've previously formatted. This is handled by formatDevice() which
	// checks the formatted volumes registry before proceeding.
	//
	// If the last error was "no filesystem", device *might* need formatting
	if len(lastOutput) == 0 || strings.Contains(string(lastOutput), "does not contain") {
		klog.Warningf("Device %s still shows no filesystem after %d retries, MAY need formatting", devicePath, maxRetries)
		return true, nil
	}

	// Device still not ready - this is unexpected
	return false, fmt.Errorf("%w: device %s not ready after %d retries: %w (output: %s)",
		ErrDeviceNotReady, devicePath, maxRetries, lastErr, string(lastOutput))
}

// formatDevice formats a device with the specified filesystem.
// CRITICAL DATA LOSS PREVENTION: This function checks the formatted volumes registry
// to ensure we NEVER reformat a volume that has been previously formatted, even if
// we can't currently detect its filesystem (e.g., due to timing issues with blkid).
func formatDevice(ctx context.Context, volumeID, devicePath, fsType string) error {
	// LAYER 3 DEFENSE: Check if this volume was previously formatted
	// If yes, REFUSE to format it - this prevents data loss even if blkid fails
	registry := getFormattedVolumesRegistry()
	wasFormatted, existingEntry := registry.WasFormatted(volumeID)

	if wasFormatted {
		// CRITICAL: This volume was previously formatted. NEVER reformat it.
		// This could happen if:
		// 1. Device reconnected and metadata isn't immediately visible to blkid
		// 2. Transient kernel cache issues
		// 3. Network connectivity issues with NVMe-oF
		//
		// In all cases, reformatting would destroy user data - ABORT.
		klog.Errorf("REFUSING to format volume %s: it was previously formatted on %v (device: %s, fs: %s). "+
			"This volume may contain user data. If filesystem detection failed, this is likely a timing issue with blkid.",
			volumeID, existingEntry.FormattedAt, existingEntry.DevicePath, existingEntry.FilesystemType)
		return fmt.Errorf("%w: volume %s was formatted on %v (would cause data loss)",
			ErrVolumeAlreadyFormatted, volumeID, existingEntry.FormattedAt)
	}

	// Volume was never formatted before - safe to format
	klog.Infof("Formatting new volume %s at %s with filesystem %s (not in formatted volumes registry)",
		volumeID, devicePath, fsType)

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

	// Record this volume as formatted in the registry
	// This ensures we'll never accidentally reformat it in the future
	if recordErr := registry.RecordFormatted(volumeID, devicePath, fsType); recordErr != nil {
		// Log error but don't fail - the volume is already formatted successfully
		klog.Errorf("Failed to record formatted volume %s in registry (volume is formatted but won't be protected from future reformats): %v",
			volumeID, recordErr)
	}

	return nil
}
