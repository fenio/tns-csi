package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Block-device helpers shared by the iSCSI and NVMe-oF node paths.
// Anything in here is protocol-agnostic — both node_iscsi.go and node_nvmeof.go
// call into these functions during NodeStageVolume to bring up, format, and
// verify the LUN that's been attached to the host.

// waitForDeviceInitialization waits for a freshly attached block device to be fully
// initialized. A device is considered initialized when it reports a non-zero size.
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
// On transient "device busy" errors from mke2fs (the BLKRRPART EBUSY check, typically
// caused by udev/blkid briefly scanning the new LUN), we wait, re-run the filesystem
// detection, and retry. We deliberately do not pass mke2fs a second -F to bypass that
// check — if udev's scan eventually surfaces a filesystem we initially missed, we
// preserve it instead of destroying data.
func (s *NodeService) handleDeviceFormatting(ctx context.Context, volumeID, devicePath, fsType, datasetName, nqn string, isClone bool) error {
	needsFormat, err := needsFormatWithRetries(ctx, devicePath, isClone)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if !needsFormat {
		klog.V(4).Infof("Device %s is already formatted, preserving existing filesystem (dataset: %s, NQN: %s)",
			devicePath, datasetName, nqn)
		return nil
	}

	klog.V(4).Infof("Device %s needs formatting with %s (dataset: %s)", devicePath, fsType, datasetName)

	const maxFormatAttempts = 5
	backoff := 2 * time.Second
	var lastErr error
	for attempt := 1; attempt <= maxFormatAttempts; attempt++ {
		formatErr := formatDevice(ctx, volumeID, devicePath, fsType)
		if formatErr == nil {
			return nil
		}
		lastErr = formatErr
		if !isDeviceBusyError(formatErr) {
			return status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}

		klog.Warningf("Format attempt %d/%d for %s hit transient device-busy, waiting %v then re-checking filesystem",
			attempt, maxFormatAttempts, devicePath, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2

		recheck, recheckErr := needsFormatWithRetries(ctx, devicePath, isClone)
		if recheckErr == nil && !recheck {
			klog.Infof("Device %s detected as already formatted on recheck after busy error — preserving existing filesystem", devicePath)
			return nil
		}
	}
	return status.Errorf(codes.Internal, "Failed to format device after %d attempts: %v", maxFormatAttempts, lastErr)
}

// logDeviceInfo logs detailed information about a block device for troubleshooting.
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
