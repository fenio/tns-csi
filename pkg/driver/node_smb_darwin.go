//go:build darwin

package driver

// Default SMB/CIFS mount options for macOS.
var defaultSMBMountOptions = []string{}

// getSMBMountOptions merges user-provided mount options with sensible defaults.
// User options take precedence over defaults.
func getSMBMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultSMBMountOptions
	}
	return userOptions
}
