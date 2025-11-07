//go:build darwin

package driver

// getNFSMountOptions returns platform-specific NFS mount options.
// macOS supports NFSv3 and NFSv4 (but not v4.2).
func getNFSMountOptions() []string {
	return []string{"vers=4", "nolock"}
}
