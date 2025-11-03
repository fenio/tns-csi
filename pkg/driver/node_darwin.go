//go:build darwin

package driver

import "syscall"

// getBlockSize returns the block size from statfs in a platform-safe way.
// On Darwin, Bsize is uint32, so conversion to uint64 is always safe.
func getBlockSize(statfs *syscall.Statfs_t) uint64 {
	return uint64(statfs.Bsize)
}
