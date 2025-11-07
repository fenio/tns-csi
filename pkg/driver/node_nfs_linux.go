//go:build !darwin

package driver

// getNFSMountOptions returns platform-specific NFS mount options.
// Linux supports NFSv4.2.
func getNFSMountOptions() []string {
	return []string{"vers=4.2", "nolock"}
}
