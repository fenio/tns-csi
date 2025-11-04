package mount

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// IsMounted checks if a path is mounted.
func IsMounted(ctx context.Context, targetPath string) (bool, error) {
	// Use findmnt to check if path is mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "TARGET", "-n", "-l", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero exit code if path is not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// IsDeviceMounted checks if a device path is mounted (for block devices).
func IsDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	// For block devices, check if it's bind mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "SOURCE", "-n", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero if not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// Unmount unmounts a path.
func Unmount(ctx context.Context, targetPath string) error {
	umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // umount command with path variable is expected for CSI driver
	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unmount: %w, output: %s", err, string(output))
	}
	return nil
}

// JoinMountOptions joins mount options with commas.
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
