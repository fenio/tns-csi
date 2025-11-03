//go:build linux

package driver

import "syscall"

// getBlockSize returns the block size from statfs in a platform-safe way.
// On Linux, Bsize is int64. Block sizes are always positive for mounted
// filesystems, so we check for negative values (which would be invalid).
func getBlockSize(statfs *syscall.Statfs_t) uint64 {
	if statfs.Bsize < 0 {
		// This should never happen for a valid mounted filesystem
		return 0
	}
	return uint64(statfs.Bsize)
}
