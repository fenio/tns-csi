//go:build darwin

// Package mount provides macOS stub implementations for mount utilities.
// The CSI driver only runs on Linux, but these stubs allow building and testing on macOS.
package mount

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned when a function is not implemented for this platform.
var ErrNotImplemented = errors.New("not implemented on darwin")

// IsMounted checks if a path is mounted.
// This is a stub implementation for macOS.
func IsMounted(_ context.Context, _ string) (bool, error) {
	return false, ErrNotImplemented
}

// IsDeviceMounted checks if a device path is mounted (for block devices).
// This is a stub implementation for macOS.
func IsDeviceMounted(_ context.Context, _ string) (bool, error) {
	return false, ErrNotImplemented
}

// Unmount unmounts a path.
// This is a stub implementation for macOS.
func Unmount(_ context.Context, _ string) error {
	return ErrNotImplemented
}

// IsStaleNFSMount checks if a path has a stale NFS mount.
// This is a stub implementation for macOS.
func IsStaleNFSMount(_ context.Context, _ string) (bool, error) {
	return false, ErrNotImplemented
}

// ForceUnmount forcefully unmounts a path.
// This is a stub implementation for macOS.
func ForceUnmount(_ context.Context, _ string) error {
	return ErrNotImplemented
}

// UnmountWithRetry unmounts a path with retry logic and stale mount handling.
// This is a stub implementation for macOS.
func UnmountWithRetry(_ context.Context, _ string, _ int) error {
	return ErrNotImplemented
}
