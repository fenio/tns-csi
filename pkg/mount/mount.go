// Package mount provides common mount utilities for CSI driver operations.
package mount

import "strings"

// JoinMountOptions joins mount options with commas.
// This function is platform-independent.
func JoinMountOptions(options []string) string {
	if len(options) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(options[0])
	for i := 1; i < len(options); i++ {
		builder.WriteString(",")
		builder.WriteString(options[i])
	}
	return builder.String()
}
