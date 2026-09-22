//go:build linux

// Package mount provides Linux-specific mount utilities for CSI driver operations.
package mount

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

// IsSourceMounted checks whether a source device is mounted anywhere.
func IsSourceMounted(ctx context.Context, sourcePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		return false, fmt.Errorf("failed to stat source device: %w", err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return false, fmt.Errorf("source path %s is not a device", sourcePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to read device metadata for %s", sourcePath)
	}

	mountInfo, err := os.Open("/proc/1/mountinfo")
	if err != nil {
		return false, fmt.Errorf("failed to open host mount information: %w", err)
	}
	defer mountInfo.Close()

	return isDeviceInMountInfo(unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)), mountInfo)
}

func isDeviceInMountInfo(deviceMajor, deviceMinor uint32, mountInfo io.Reader) (bool, error) {
	scanner := bufio.NewScanner(mountInfo)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			return false, fmt.Errorf("invalid mountinfo entry %q", scanner.Text())
		}
		deviceNumbers := strings.Split(fields[2], ":")
		if len(deviceNumbers) != 2 {
			return false, fmt.Errorf("invalid mountinfo device number %q", fields[2])
		}
		major, err := strconv.ParseUint(deviceNumbers[0], 10, 32)
		if err != nil {
			return false, fmt.Errorf("invalid mountinfo major number %q: %w", deviceNumbers[0], err)
		}
		minor, err := strconv.ParseUint(deviceNumbers[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("invalid mountinfo minor number %q: %w", deviceNumbers[1], err)
		}
		if uint32(major) == deviceMajor && uint32(minor) == deviceMinor {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("failed to read host mount information: %w", err)
	}
	return false, nil
}

// Unmount unmounts a path.
func Unmount(ctx context.Context, targetPath string) error {
	umountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unmount: %w, output: %s", err, string(output))
	}
	return nil
}
