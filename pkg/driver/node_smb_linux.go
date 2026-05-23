//go:build !darwin

package driver

// Default SMB/CIFS mount options for Linux.
// file_mode/dir_mode=0777 ensures containers can read/write regardless of UID
// (CIFS doesn't support POSIX ownership like NFS; permissions are set at mount time).
// nosharesock: force a dedicated TCP connection per mount — prevents mount failures
// when the CIFS client reuses a session that received a negative tree connect response
// (STATUS_BAD_NETWORK_NAME) before a new share was fully registered in Samba's config.
const smbVersDefault = "vers=3.0"

var defaultSMBMountOptions = []string{smbVersDefault, "nosharesock", "file_mode=0777", "dir_mode=0777"}

// getSMBMountOptions merges user-provided mount options with sensible defaults.
// User options take precedence over defaults.
func getSMBMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultSMBMountOptions
	}

	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractOptionKey(opt)
		userOptionKeys[key] = true
	}

	result := make([]string, 0, len(userOptions)+len(defaultSMBMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultSMBMountOptions {
		key := extractOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}
