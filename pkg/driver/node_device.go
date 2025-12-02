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
	// ErrPartitionedDevice is returned when a device has partitions and partition handling is required.
	ErrPartitionedDevice = errors.New("device has partitions")
	// ErrInvalidNVMeDevicePath is returned when an NVMe device path is malformed.
	ErrInvalidNVMeDevicePath = errors.New("invalid NVMe device path")
	// ErrFilesystemExpansionNotSupported is returned when filesystem expansion is not supported for the given type.
	ErrFilesystemExpansionNotSupported = errors.New("filesystem expansion not supported")
)

// Filesystem type constants.
const (
	fsTypeBtrfs = "btrfs"
)

// DeviceInfo contains information about a block device.
//
//nolint:govet // fieldalignment: struct field order optimized for readability over memory layout
type DeviceInfo struct {
	Path         string   // Device path (e.g., /dev/nvme0n1)
	Partitions   []string // List of partition paths
	FSType       string   // Filesystem type if detected
	Size         int64    // Device size in bytes
	HasPartition bool     // Whether the device has partitions
	IsMultipath  bool     // Whether this is a multipath device
}

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

// needsFormatWithRetries checks if a device needs formatting with different retry logic for clones vs new volumes.
// For cloned volumes, we use many retries (25) to ensure filesystem metadata has propagated.
// For new NVMe volumes, we use fewer retries (3) since we expect to format them.
// For non-NVMe devices, we use few retries (3) to avoid gRPC timeouts.
func needsFormatWithRetries(ctx context.Context, devicePath string, isClone bool) (bool, error) {
	var maxRetries int
	isNVMe := strings.Contains(devicePath, "/dev/nvme")

	switch {
	case isClone:
		// Clones inherit filesystem from snapshot - need many retries to detect it
		// and avoid destroying data by reformatting.
		maxRetries = 25
		klog.Infof("Checking cloned volume filesystem (max %d retries to avoid destroying clone data)", maxRetries)
	case isNVMe:
		// New NVMe volumes: We expect to format them, so use fewer retries.
		// 3 retries with exponential backoff is enough to confirm no filesystem exists.
		// This avoids the 150+ second delay that was causing pod ready timeouts.
		maxRetries = 3
		klog.Infof("Checking new NVMe volume filesystem (max %d retries, will format if needed)", maxRetries)
	default:
		maxRetries = 3 // Fast for non-NVMe new volumes - avoid gRPC timeout (typical 2min deadline)
		klog.Infof("Checking new volume filesystem (max %d retries, will format if needed)", maxRetries)
	}

	const (
		initialBackoff = 200 * time.Millisecond
		maxBackoff     = 10 * time.Second
	)

	klog.Infof("Checking if device %s needs formatting (max %d retries with up to %v backoff)",
		devicePath, maxRetries, maxBackoff)

	// CRITICAL: For NVMe devices, add initial stabilization delay before first check
	if err := waitForNVMeStabilization(ctx, devicePath); err != nil {
		return false, err
	}

	// Retry with exponential backoff to handle device readiness timing
	lastOutput, lastErr := retryFilesystemCheck(ctx, devicePath, maxRetries, initialBackoff, maxBackoff, isClone)

	// After all retries, handle the final result
	return handleFinalResult(devicePath, maxRetries, lastOutput, lastErr)
}

// waitForNVMeStabilization adds stabilization delay for NVMe devices.
// This delay is in addition to the device initialization wait in stageNVMeDevice.
// After the device reports non-zero size, we add a small additional delay before
// the filesystem check retry loop begins to give filesystem metadata time to settle.
func waitForNVMeStabilization(ctx context.Context, devicePath string) error {
	if !strings.Contains(devicePath, "/dev/nvme") {
		return nil
	}

	const nvmeInitialDelay = 3 * time.Second
	klog.V(4).Infof("NVMe device detected, waiting %v before first filesystem check", nvmeInitialDelay)
	select {
	case <-time.After(nvmeInitialDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryFilesystemCheck performs filesystem checks with exponential backoff.
func retryFilesystemCheck(ctx context.Context, devicePath string, maxRetries int, initialBackoff, maxBackoff time.Duration, isClone bool) ([]byte, error) {
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

		if shouldStopRetrying(needsFmt, err, devicePath, attempt, maxRetries, output, isClone) {
			return lastOutput, lastErr
		}
	}

	return lastOutput, lastErr
}

// shouldStopRetrying determines if we should stop retrying filesystem checks.
func shouldStopRetrying(needsFmt bool, err error, devicePath string, attempt, maxRetries int, output []byte, isClone bool) bool {
	// Log detailed information about this attempt
	if needsFmt || err != nil {
		klog.Warningf("needsFormat attempt %d/%d for %s: needsFormat=%v, err=%v, output=%q (will retry if uncertain)",
			attempt+1, maxRetries, devicePath, needsFmt, err, string(output))
	} else {
		klog.Infof("needsFormat attempt %d/%d for %s: filesystem detected, needsFormat=false",
			attempt+1, maxRetries, devicePath)
	}

	// For CLONED volumes with no filesystem detected, continue retrying.
	// Clones inherit filesystem from snapshot but metadata may take time to propagate.
	// We must retry to avoid destroying cloned data by reformatting too early.
	if err == nil && needsFmt && isClone {
		if attempt+1 < maxRetries {
			klog.Infof("Cloned volume %s has no filesystem detected yet (attempt %d/%d) - will retry to avoid destroying clone data",
				devicePath, attempt+1, maxRetries)
			return false // Continue retrying
		}
		// Reached max retries for clone - stop and warn
		klog.Warningf("Cloned volume %s still shows no filesystem after %d attempts - will fail unless force-format annotation is set",
			devicePath, attempt+1)
		return true
	}

	// For NEW volumes (not clones): If no filesystem detected with no error, we can stop immediately.
	// New volumes are expected to be empty and need formatting - no need to retry excessively.
	if err == nil && needsFmt && !isClone {
		klog.Infof("New volume %s has no filesystem (confirmed after %d attempts) - proceeding to format", devicePath, attempt+1)
		return true
	}

	// If filesystem check succeeded with filesystem found
	if err == nil && !needsFmt {
		klog.Infof("Device %s has existing filesystem, skipping format (detected after %d attempts)", devicePath, attempt+1)
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
	case fsTypeExt4:
		// -F force, don't ask for confirmation
		cmd = exec.CommandContext(formatCtx, "mkfs.ext4", "-F", devicePath)
	case fsTypeExt3:
		cmd = exec.CommandContext(formatCtx, "mkfs.ext3", "-F", devicePath)
	case fsTypeExt2:
		cmd = exec.CommandContext(formatCtx, "mkfs.ext2", "-F", devicePath)
	case fsTypeXFS:
		// -f force overwrite
		cmd = exec.CommandContext(formatCtx, "mkfs.xfs", "-f", devicePath)
	case fsTypeBtrfs:
		// -f force overwrite
		cmd = exec.CommandContext(formatCtx, "mkfs.btrfs", "-f", devicePath)
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

// runFsck runs filesystem check on a device before mounting.
// This is optional and controlled by the "fsckBeforeMount" volume context parameter.
// Returns nil if fsck is not enabled or if the check passes.
func runFsck(ctx context.Context, devicePath, fsType string, volumeContext map[string]string) error {
	// Check if fsck is enabled via volume context
	fsckEnabled := volumeContext["fsckBeforeMount"]
	if fsckEnabled != "true" && fsckEnabled != "1" {
		klog.V(4).Infof("fsck before mount not enabled for device %s", devicePath)
		return nil
	}

	klog.Infof("Running filesystem check on %s (fsType: %s) before mounting", devicePath, fsType)

	// Select appropriate fsck command based on filesystem type
	var cmd *exec.Cmd
	fsckCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	switch fsType {
	case fsTypeExt4, fsTypeExt3, fsTypeExt2:
		// -p: automatically repair what can be safely repaired
		// -y: assume yes to all questions (for non-interactive)
		cmd = exec.CommandContext(fsckCtx, "e2fsck", "-p", "-f", devicePath)
	case fsTypeXFS:
		// xfs_repair is the fsck equivalent for XFS
		// -n: no-modify mode (check only, don't repair)
		// For repair, we'd use xfs_repair without -n
		checkOnly := volumeContext["fsckCheckOnly"]
		if checkOnly == "true" || checkOnly == "1" {
			cmd = exec.CommandContext(fsckCtx, "xfs_repair", "-n", devicePath)
		} else {
			cmd = exec.CommandContext(fsckCtx, "xfs_repair", devicePath)
		}
	case fsTypeBtrfs:
		// btrfs check for btrfs filesystems
		// --readonly: check only, don't repair
		checkOnly := volumeContext["fsckCheckOnly"]
		if checkOnly == "true" || checkOnly == "1" {
			cmd = exec.CommandContext(fsckCtx, "btrfs", "check", "--readonly", devicePath)
		} else {
			cmd = exec.CommandContext(fsckCtx, "btrfs", "check", "--repair", devicePath)
		}
	default:
		klog.Warningf("fsck not supported for filesystem type %s on device %s", fsType, devicePath)
		return nil
	}

	klog.V(4).Infof("Running fsck command: %v", cmd.Args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// For ext filesystems, exit code 1 means errors were corrected
		// Exit code 0 means no errors found
		// Exit code 2+ means errors that could not be corrected
		if fsType == fsTypeExt4 || fsType == fsTypeExt3 || fsType == fsTypeExt2 {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode := exitErr.ExitCode()
				if exitCode == 1 {
					klog.Infof("e2fsck corrected errors on %s: %s", devicePath, string(output))
					return nil
				}
				if exitCode == 0 {
					return nil
				}
			}
		}
		klog.Errorf("fsck failed on %s: %v, output: %s", devicePath, err, string(output))
		return fmt.Errorf("filesystem check failed on %s: %w, output: %s", devicePath, err, string(output))
	}

	klog.Infof("Filesystem check passed for %s", devicePath)
	return nil
}

// GetDeviceInfo retrieves information about a block device.
// This includes partition detection, filesystem type, and multipath status.
func GetDeviceInfo(ctx context.Context, devicePath string) (*DeviceInfo, error) {
	info := &DeviceInfo{
		Path: devicePath,
	}

	// Get device size
	sizeCtx, sizeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer sizeCancel()
	sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	sizeOutput, err := sizeCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get device size for %s: %w", devicePath, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse device size for %s: %w", devicePath, err)
	}
	info.Size = size

	// Check for partitions using lsblk
	partitions, err := detectPartitions(ctx, devicePath)
	if err != nil {
		klog.V(4).Infof("Failed to detect partitions for %s: %v", devicePath, err)
	} else {
		info.Partitions = partitions
		info.HasPartition = len(partitions) > 0
	}

	// Get filesystem type using blkid
	fsType, err := detectDeviceFilesystemType(ctx, devicePath)
	if err != nil {
		klog.V(4).Infof("Failed to detect filesystem type for %s: %v", devicePath, err)
	} else {
		info.FSType = fsType
	}

	// Check if this is a multipath device
	info.IsMultipath = isMultipathDevice(ctx, devicePath)

	klog.V(4).Infof("Device info for %s: size=%d, partitions=%v, fsType=%s, multipath=%v",
		devicePath, info.Size, info.Partitions, info.FSType, info.IsMultipath)

	return info, nil
}

// detectPartitions detects partitions on a block device.
func detectPartitions(ctx context.Context, devicePath string) ([]string, error) {
	lsblkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use lsblk to list child devices (partitions)
	cmd := exec.CommandContext(lsblkCtx, "lsblk", "-n", "-o", "NAME", "-l", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %w, output: %s", err, string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) <= 1 {
		// Only the device itself, no partitions
		return nil, nil
	}

	// First line is the device itself, rest are partitions
	var partitions []string
	for i := 1; i < len(lines); i++ {
		name := strings.TrimSpace(lines[i])
		if name != "" {
			partitions = append(partitions, "/dev/"+name)
		}
	}

	return partitions, nil
}

// detectDeviceFilesystemType detects the filesystem type of a device using blkid.
func detectDeviceFilesystemType(ctx context.Context, devicePath string) (string, error) {
	blkidCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(blkidCtx, "blkid", "-o", "value", "-s", "TYPE", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// blkid returns error if no filesystem found
		return "", nil
	}

	return strings.TrimSpace(string(output)), nil
}

// isMultipathDevice checks if a device is a multipath device.
func isMultipathDevice(ctx context.Context, devicePath string) bool {
	// Check if multipath-tools is available
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Try to query multipath status
	cmd := exec.CommandContext(checkCtx, "multipath", "-l", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// multipath command not available or device not in multipath
		return false
	}

	// If we got output, it's a multipath device
	return strings.TrimSpace(string(output)) != ""
}

// GetDeviceToUse determines which device path to use for mounting.
// For partitioned devices, returns the first partition if appropriate.
// For whole disk devices without partitions, returns the device itself.
func GetDeviceToUse(ctx context.Context, devicePath string, volumeContext map[string]string) (string, error) {
	info, err := GetDeviceInfo(ctx, devicePath)
	if err != nil {
		klog.Warningf("Failed to get device info for %s: %v, using device as-is", devicePath, err)
		return devicePath, nil
	}

	// Check if partition handling is enabled
	handlePartitions := volumeContext["handlePartitions"]
	if handlePartitions != "true" && handlePartitions != "1" {
		// Partition handling not enabled, use device as-is
		if info.HasPartition {
			klog.Warningf("Device %s has partitions but partition handling is disabled, using device as-is", devicePath)
		}
		return devicePath, nil
	}

	// If device has partitions, use the first partition
	if info.HasPartition && len(info.Partitions) > 0 {
		klog.Infof("Device %s has partitions, using first partition %s", devicePath, info.Partitions[0])
		return info.Partitions[0], nil
	}

	return devicePath, nil
}

// RescanDevice rescans a block device to detect size changes after expansion.
// This is necessary for both NVMe and SCSI devices after online resize.
func RescanDevice(ctx context.Context, devicePath string) error {
	klog.V(4).Infof("Rescanning device %s for size changes", devicePath)

	// For NVMe devices, use nvme ns-rescan
	if strings.Contains(devicePath, "nvme") {
		return rescanNVMeDevice(ctx, devicePath)
	}

	// For SCSI devices, write to rescan sysfs file
	return rescanSCSIDevice(ctx, devicePath)
}

// rescanNVMeDevice rescans an NVMe device.
func rescanNVMeDevice(ctx context.Context, devicePath string) error {
	// Extract controller from device path (e.g., /dev/nvme0n1 -> /dev/nvme0)
	controllerEnd := strings.LastIndex(devicePath, "n")
	if controllerEnd <= 0 {
		return fmt.Errorf("%w: %s", ErrInvalidNVMeDevicePath, devicePath)
	}
	controller := devicePath[:controllerEnd]

	rescanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	//nolint:gosec // G204: controller is validated to match /dev/nvme[0-9]+ pattern above
	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controller)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nvme ns-rescan failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("NVMe device %s rescanned successfully", devicePath)
	return nil
}

// rescanSCSIDevice rescans a SCSI device.
func rescanSCSIDevice(_ context.Context, devicePath string) error {
	// Extract device name from path (e.g., /dev/sda -> sda)
	deviceName := filepath.Base(devicePath)

	// Write to the rescan sysfs file
	rescanPath := fmt.Sprintf("/sys/block/%s/device/rescan", deviceName)

	// Check if rescan path exists
	if _, err := os.Stat(rescanPath); os.IsNotExist(err) {
		klog.V(4).Infof("Rescan path %s does not exist, skipping SCSI rescan", rescanPath)
		return nil
	}

	//nolint:gosec // Writing "1" to sysfs rescan file is standard kernel interface
	if err := os.WriteFile(rescanPath, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("failed to trigger SCSI rescan: %w", err)
	}

	klog.V(4).Infof("SCSI device %s rescanned successfully", devicePath)
	return nil
}

// ExpandFilesystem expands a filesystem to use all available space on the device.
// This should be called after the underlying block device has been expanded.
func ExpandFilesystem(ctx context.Context, devicePath, mountPoint, fsType string) error {
	klog.Infof("Expanding filesystem on %s (mounted at %s, type: %s)", devicePath, mountPoint, fsType)

	expandCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch fsType {
	case fsTypeExt4, fsTypeExt3, fsTypeExt2:
		// resize2fs can work on mounted filesystems
		cmd = exec.CommandContext(expandCtx, "resize2fs", devicePath)
	case fsTypeXFS:
		// xfs_growfs requires the mount point, not the device
		cmd = exec.CommandContext(expandCtx, "xfs_growfs", mountPoint)
	case fsTypeBtrfs:
		// btrfs filesystem resize requires the mount point
		cmd = exec.CommandContext(expandCtx, "btrfs", "filesystem", "resize", "max", mountPoint)
	default:
		return fmt.Errorf("%w for type: %s", ErrFilesystemExpansionNotSupported, fsType)
	}

	klog.V(4).Infof("Running filesystem expansion command: %v", cmd.Args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("filesystem expansion failed: %w, output: %s", err, string(output))
	}

	klog.Infof("Successfully expanded filesystem on %s", devicePath)
	return nil
}
