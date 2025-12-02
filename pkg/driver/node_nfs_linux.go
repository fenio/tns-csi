//go:build !darwin

package driver

import "strings"

// DefaultNFSMountOptions are the default NFS mount options for Linux.
// These are used when no custom options are specified in the StorageClass.
var DefaultNFSMountOptions = []string{"vers=4.2", "nolock"}

// getNFSMountOptions returns platform-specific NFS mount options.
// Linux supports NFSv4.2.
func getNFSMountOptions() []string {
	return DefaultNFSMountOptions
}

// parseNFSMountOptions parses custom NFS mount options from volume context.
// It merges custom options with defaults, with custom options taking precedence.
// Format: comma-separated list of options, e.g., "vers=4.1,soft,timeo=30"
func parseNFSMountOptions(volumeContext map[string]string) []string {
	customOptions := volumeContext["nfsMountOptions"]
	if customOptions == "" {
		return getNFSMountOptions()
	}

	// Parse custom options
	customList := strings.Split(customOptions, ",")
	result := make([]string, 0, len(customList))

	// Build a map to track which option keys are overridden
	overriddenKeys := make(map[string]bool)
	for _, opt := range customList {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		result = append(result, opt)

		// Extract option key (part before '=' if present)
		key := opt
		if idx := strings.Index(opt, "="); idx > 0 {
			key = opt[:idx]
		}
		overriddenKeys[key] = true
	}

	// Add default options that aren't overridden
	for _, defaultOpt := range DefaultNFSMountOptions {
		key := defaultOpt
		if idx := strings.Index(defaultOpt, "="); idx > 0 {
			key = defaultOpt[:idx]
		}
		if !overriddenKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}
