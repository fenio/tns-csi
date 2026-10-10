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

var (
	errInvalidSourceDevice = errors.New("invalid source device")
	errInvalidMountInfo    = errors.New("invalid mount information")
)

// IsMounted reports whether targetPath is a mount point. It reads the mount table
// (see MountedEntry) and returns an error, never false, when that table cannot be read.
func IsMounted(ctx context.Context, targetPath string) (bool, error) {
	_, mounted, err := MountedEntry(ctx, targetPath)
	if err != nil {
		return false, fmt.Errorf("failed to check mount: %w", err)
	}
	return mounted, nil
}

// IsDeviceMounted reports whether targetPath (a block-volume publish target) is a
// mount point. Block volumes are bind mounts, so this is the same check as IsMounted.
func IsDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	return IsMounted(ctx, targetPath)
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
		return false, fmt.Errorf("%w: path %s is not a device", errInvalidSourceDevice, sourcePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%w: failed to read metadata for %s", errInvalidSourceDevice, sourcePath)
	}

	mountInfo, err := os.Open("/proc/1/mountinfo")
	if err != nil {
		return false, fmt.Errorf("failed to open host mount information: %w", err)
	}
	mounted, parseErr := isDeviceInMountInfo(unix.Major(stat.Rdev), unix.Minor(stat.Rdev), mountInfo)
	closeErr := mountInfo.Close()
	if parseErr != nil {
		return false, parseErr
	}
	if closeErr != nil {
		return false, fmt.Errorf("failed to close host mount information: %w", closeErr)
	}
	return mounted, nil
}

func isDeviceInMountInfo(deviceMajor, deviceMinor uint32, mountInfo io.Reader) (bool, error) {
	scanner := bufio.NewScanner(mountInfo)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			return false, fmt.Errorf("%w: entry %q", errInvalidMountInfo, scanner.Text())
		}
		deviceNumbers := strings.Split(fields[2], ":")
		if len(deviceNumbers) != 2 {
			return false, fmt.Errorf("%w: device number %q", errInvalidMountInfo, fields[2])
		}
		major, err := strconv.ParseUint(deviceNumbers[0], 10, 32)
		if err != nil {
			return false, fmt.Errorf("invalid mountinfo major number %q: %w", deviceNumbers[0], err)
		}
		minor, err := strconv.ParseUint(deviceNumbers[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("invalid mountinfo minor number %q: %w", deviceNumbers[1], err)
		}
		if major == uint64(deviceMajor) && minor == uint64(deviceMinor) {
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
