//go:build !darwin

package driver

// Default SMB/CIFS mount options for Linux.
var defaultSMBMountOptions = []string{"vers=3.0"}

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
