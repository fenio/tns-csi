//go:build linux

// Package mount provides Linux-specific mount utilities for CSI driver operations.
package mount

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// IsMounted checks if a path is mounted.
func IsMounted(ctx context.Context, targetPath string) (bool, error) {
	// Use findmnt to check if path is mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "TARGET", "-n", "-l", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero exit code if path is not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// IsDeviceMounted checks if a device path is mounted (for block devices).
func IsDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	// For block devices, check if it's bind mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "SOURCE", "-n", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero if not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// Unmount unmounts a path.
func Unmount(ctx context.Context, targetPath string) error {
	umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unmount: %w, output: %s", err, string(output))
	}
	return nil
}

// IsStaleNFSMount checks if a path has a stale NFS mount.
// A stale NFS mount occurs when the NFS server becomes unreachable but the mount
// point still exists. Accessing such a mount causes I/O errors or hangs.
func IsStaleNFSMount(ctx context.Context, targetPath string) (bool, error) {
	// Use stat with a short timeout - stale mounts will hang
	statCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// First, check if it's an NFS mount
	fsTypeCmd := exec.CommandContext(statCtx, "findmnt", "-o", "FSTYPE", "-n", targetPath)
	fsTypeOutput, err := fsTypeCmd.CombinedOutput()
	if err != nil {
		// If findmnt fails, the path might not be mounted
		return false, nil
	}

	fsType := strings.TrimSpace(string(fsTypeOutput))
	if !strings.HasPrefix(fsType, "nfs") {
		// Not an NFS mount
		return false, nil
	}

	// Try to stat the mount point with timeout
	// Stale NFS mounts will hang on stat, causing context timeout
	statCmd := exec.CommandContext(statCtx, "stat", "-f", targetPath)
	_, statErr := statCmd.CombinedOutput()
	if statErr != nil {
		// Check if it's a context timeout (indicates stale mount)
		if statCtx.Err() == context.DeadlineExceeded {
			klog.Warningf("Detected stale NFS mount at %s (stat timed out)", targetPath)
			return true, nil
		}
		// Other error - might be stale, might be something else
		// Check for specific stale mount indicators
		if strings.Contains(statErr.Error(), "Stale file handle") ||
			strings.Contains(statErr.Error(), "No route to host") ||
			strings.Contains(statErr.Error(), "Connection timed out") {
			klog.Warningf("Detected stale NFS mount at %s: %v", targetPath, statErr)
			return true, nil
		}
	}

	return false, nil
}

// ForceUnmount forcefully unmounts a path.
// This is used for stale NFS mounts that cannot be unmounted normally.
func ForceUnmount(ctx context.Context, targetPath string) error {
	klog.Infof("Attempting force unmount of %s", targetPath)

	// Try lazy unmount first (-l flag)
	lazyCtx, lazyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer lazyCancel()
	lazyCmd := exec.CommandContext(lazyCtx, "umount", "-l", targetPath)
	lazyOutput, lazyErr := lazyCmd.CombinedOutput()
	if lazyErr == nil {
		klog.Infof("Lazy unmount succeeded for %s", targetPath)
		return nil
	}
	klog.Warningf("Lazy unmount failed for %s: %v, output: %s", targetPath, lazyErr, string(lazyOutput))

	// Try force unmount (-f flag) - more aggressive
	forceCtx, forceCancel := context.WithTimeout(ctx, 10*time.Second)
	defer forceCancel()
	forceCmd := exec.CommandContext(forceCtx, "umount", "-f", targetPath)
	forceOutput, forceErr := forceCmd.CombinedOutput()
	if forceErr == nil {
		klog.Infof("Force unmount succeeded for %s", targetPath)
		return nil
	}
	klog.Warningf("Force unmount failed for %s: %v, output: %s", targetPath, forceErr, string(forceOutput))

	// Try both flags together as last resort
	bothCtx, bothCancel := context.WithTimeout(ctx, 10*time.Second)
	defer bothCancel()
	bothCmd := exec.CommandContext(bothCtx, "umount", "-l", "-f", targetPath)
	bothOutput, bothErr := bothCmd.CombinedOutput()
	if bothErr != nil {
		return fmt.Errorf("all unmount attempts failed for %s: lazy=%v, force=%v, both=%v (output: %s)",
			targetPath, lazyErr, forceErr, bothErr, string(bothOutput))
	}

	klog.Infof("Force+lazy unmount succeeded for %s", targetPath)
	return nil
}

// UnmountWithRetry unmounts a path with retry logic and stale mount handling.
// It first tries normal unmount, then checks for stale mounts and force unmounts if needed.
func UnmountWithRetry(ctx context.Context, targetPath string, maxRetries int) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			klog.V(4).Infof("Unmount retry %d/%d for %s", attempt+1, maxRetries, targetPath)
			time.Sleep(time.Duration(attempt) * time.Second) // Exponential backoff
		}

		// First, try normal unmount
		err := Unmount(ctx, targetPath)
		if err == nil {
			return nil
		}
		lastErr = err

		// Check if this is a stale NFS mount
		isStale, staleErr := IsStaleNFSMount(ctx, targetPath)
		if staleErr != nil {
			klog.Warningf("Failed to check for stale mount at %s: %v", targetPath, staleErr)
		}

		if isStale {
			klog.Warningf("Detected stale NFS mount at %s, attempting force unmount", targetPath)
			if forceErr := ForceUnmount(ctx, targetPath); forceErr == nil {
				return nil
			} else {
				lastErr = forceErr
			}
		}

		// Check if mount point still exists
		mounted, mountErr := IsMounted(ctx, targetPath)
		if mountErr != nil {
			klog.Warningf("Failed to check mount status for %s: %v", targetPath, mountErr)
			continue
		}
		if !mounted {
			// Successfully unmounted (or was never mounted)
			return nil
		}
	}

	return fmt.Errorf("failed to unmount %s after %d attempts: %w", targetPath, maxRetries, lastErr)
}
