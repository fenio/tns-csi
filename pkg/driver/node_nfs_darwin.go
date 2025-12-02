//go:build darwin

package driver

import "strings"

// DefaultNFSMountOptions are the default NFS mount options for macOS.
// This is a stub for building on macOS - the actual driver runs on Linux.
var DefaultNFSMountOptions = []string{"vers=4", "nolock"}

// getNFSMountOptions returns platform-specific NFS mount options.
// This is a stub for building on macOS.
func getNFSMountOptions() []string {
	return DefaultNFSMountOptions
}

// parseNFSMountOptions parses custom NFS mount options from volume context.
// This is a stub for building on macOS - the actual driver runs on Linux.
func parseNFSMountOptions(volumeContext map[string]string) []string {
	customOptions := volumeContext[VolumeContextKeyNFSMountOptions]
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
