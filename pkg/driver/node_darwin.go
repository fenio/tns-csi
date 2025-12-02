//go:build darwin

package driver

import "syscall"

// getBlockSize returns the block size from statfs in a platform-safe way.
// On macOS, Bsize is uint32. This is a stub implementation for building on macOS.
func getBlockSize(statfs *syscall.Statfs_t) uint64 {
	return uint64(statfs.Bsize)
}
