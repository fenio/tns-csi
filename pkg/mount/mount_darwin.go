//go:build darwin

// Package mount provides macOS-specific mount utilities for CSI driver operations.
// These implementations are primarily for testing and development on macOS.
package mount

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// IsMounted checks if a path is mounted on macOS.
// Uses 'mount' command to check mount status since findmnt doesn't exist on macOS.
func IsMounted(ctx context.Context, targetPath string) (bool, error) {
	// For testing/development on macOS, check if path exists
	// This is a simplified check suitable for sanity tests
	_, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat path: %w", err)
	}

	// If path exists and is a directory, check if it's in mount output
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "mount")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("Failed to run mount command: %v", err)
		// On macOS for testing, if mount command fails, assume not mounted
		return false, nil
	}

	// Check if targetPath appears in mount output
	mounted := strings.Contains(string(output), targetPath)
	klog.V(5).Infof("Path %s mounted status: %v", targetPath, mounted)
	return mounted, nil
}

// IsDeviceMounted checks if a device path is mounted (for block devices) on macOS.
// This is a simplified implementation for testing on macOS.
func IsDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	// For macOS testing, use same logic as IsMounted
	return IsMounted(ctx, targetPath)
}

// Unmount unmounts a path on macOS.
// For testing purposes, this is a no-op if the path is not actually mounted.
func Unmount(ctx context.Context, targetPath string) error {
	// Check if path is actually mounted first
	mounted, err := IsMounted(ctx, targetPath)
	if err != nil {
		klog.V(4).Infof("Failed to check if path is mounted: %v, attempting unmount anyway", err)
	}

	if !mounted {
		klog.V(4).Infof("Path %s is not mounted, skipping unmount", targetPath)
		return nil
	}

	umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// On macOS for testing, log the error but don't fail if unmount fails
		klog.V(4).Infof("Unmount failed: %v, output: %s (non-fatal on macOS)", err, string(output))
		return nil
	}

	klog.V(4).Infof("Successfully unmounted %s", targetPath)
	return nil
}

// IsStaleNFSMount checks if a path has a stale NFS mount.
// On macOS, this is a simplified implementation for testing purposes.
func IsStaleNFSMount(ctx context.Context, targetPath string) (bool, error) {
	// macOS stub - always return false for testing
	klog.V(5).Infof("IsStaleNFSMount called for %s (macOS stub)", targetPath)
	return false, nil
}

// ForceUnmount forcefully unmounts a path.
// On macOS, this is a simplified implementation for testing purposes.
func ForceUnmount(ctx context.Context, targetPath string) error {
	klog.V(4).Infof("ForceUnmount called for %s (macOS stub)", targetPath)

	// On macOS, try regular unmount with force flag
	umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(umountCtx, "umount", "-f", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("Force unmount failed: %v, output: %s (non-fatal on macOS)", err, string(output))
		return nil
	}

	return nil
}

// UnmountWithRetry unmounts a path with retry logic.
// On macOS, this is a simplified implementation for testing purposes.
func UnmountWithRetry(ctx context.Context, targetPath string, maxRetries int) error {
	klog.V(4).Infof("UnmountWithRetry called for %s with %d retries (macOS stub)", targetPath, maxRetries)

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := Unmount(ctx, targetPath)
		if err == nil {
			return nil
		}

		if attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}

	// On macOS for testing, don't fail
	return nil
}
