//go:build darwin

package driver

// Default NFS mount options for macOS.
// macOS supports NFSv3 and NFSv4 (but not v4.2).
var defaultNFSMountOptions = []string{"vers=4", "nolock"}

// getNFSMountOptions merges user-provided mount options with sensible defaults.
// User options take precedence - if a user specifies an option that conflicts
// with a default (e.g., "vers=3" vs default "vers=4"), the user's option wins.
// This allows StorageClass mountOptions to fully customize NFS mount behavior.
func getNFSMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultNFSMountOptions
	}

	// Build a map of option keys that the user has specified
	// This handles both key=value options (e.g., "vers=3") and flags (e.g., "nolock")
	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractOptionKey(opt)
		userOptionKeys[key] = true
	}

	// Start with user options, then add defaults that don't conflict
	result := make([]string, 0, len(userOptions)+len(defaultNFSMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultNFSMountOptions {
		key := extractOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}

// extractOptionKey extracts the key from a mount option.
// For "key=value" options, returns "key".
// For flag options like "nolock" or "ro", returns the flag itself.
func extractOptionKey(option string) string {
	for i, c := range option {
		if c == '=' {
			return option[:i]
		}
	}
	return option
}
