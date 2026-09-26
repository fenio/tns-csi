package driver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	blkidPath  = "/sbin/blkid"
	e2fsckPath = "/sbin/e2fsck"
)

type keyedMutexEntry struct {
	token chan struct{}
	refs  int
}

type keyedMutex struct {
	entries map[string]*keyedMutexEntry
	mu      sync.Mutex
}

func (m *keyedMutex) lock(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[string]*keyedMutexEntry)
	}
	entry := m.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		m.entries[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	select {
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			m.release(key, entry)
			return nil, err
		}
		return func() {
			entry.token <- struct{}{}
			m.release(key, entry)
		}, nil
	case <-ctx.Done():
		m.release(key, entry)
		return nil, ctx.Err()
	}
}

func (m *keyedMutex) release(key string, entry *keyedMutexEntry) {
	m.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(m.entries, key)
	}
	m.mu.Unlock()
}

func ensureStagingTarget(ctx context.Context, stagingTargetPath string) (bool, error) {
	if err := os.MkdirAll(stagingTargetPath, 0o750); err != nil {
		return false, status.Errorf(codes.Internal, "failed to create staging target path: %v", err)
	}

	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return false, status.Errorf(codes.Internal, "failed to check if staging path is mounted: %v", err)
	}
	return mounted, nil
}

func detectBlockFilesystemType(ctx context.Context, devicePath string) (string, error) {
	detectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(detectCtx, blkidPath, "-s", "TYPE", "-o", "value", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", status.Errorf(codes.FailedPrecondition,
			"failed to detect filesystem type on %s: %v, output: %s", devicePath, err, truncateCommandOutput(output))
	}

	fsType := strings.ToLower(strings.TrimSpace(string(output)))
	if fsType == "" {
		return "", status.Errorf(codes.FailedPrecondition, "no filesystem detected on %s", devicePath)
	}
	return fsType, nil
}

func (s *NodeService) checkFilesystemBeforeMount(ctx context.Context, devicePath, mode string, newlyFormatted bool) error {
	if mode != filesystemCheckModePreen || newlyFormatted {
		return nil
	}

	isSourceMounted := s.isSourceMountedFn
	if isSourceMounted == nil {
		isSourceMounted = mount.IsSourceMounted
	}
	mounted, err := isSourceMounted(ctx, devicePath)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to check whether device %s is mounted: %v", devicePath, err)
	}
	if mounted {
		return status.Errorf(codes.FailedPrecondition,
			"refusing to check filesystem on mounted device %s", devicePath)
	}

	detectFilesystem := s.detectFilesystemFn
	if detectFilesystem == nil {
		detectFilesystem = detectBlockFilesystemType
	}
	fsType, err := detectFilesystem(ctx, devicePath)
	if err != nil {
		return err
	}
	return s.runFilesystemPreen(ctx, devicePath, fsType)
}

func (s *NodeService) runFilesystemPreen(ctx context.Context, devicePath, fsType string) error {
	if fsType != fsTypeExt3 && fsType != fsTypeExt4 {
		return status.Errorf(codes.FailedPrecondition,
			"filesystemCheckMode %q is not supported for detected filesystem %q on %s",
			filesystemCheckModePreen, fsType, devicePath)
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}

	runner := s.runFilesystemCheckFn
	if runner == nil {
		runner = runE2FSCK
	}

	start := time.Now()
	output, err := runner(ctx, devicePath)
	duration := time.Since(start)
	if err == nil {
		klog.Infof("Filesystem check completed for %s (%s) in %s", devicePath, fsType, duration)
		return nil
	}

	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		exitCode := exitErr.ExitCode()
		if exitCode == 1 {
			klog.Infof("Filesystem check repaired errors on %s (%s) in %s: %s",
				devicePath, fsType, duration, truncateCommandOutput(output))
			return nil
		}
		return status.Errorf(codes.FailedPrecondition,
			"filesystem check failed for %s (%s) with exit code %d: %s",
			devicePath, fsType, exitCode, truncateCommandOutput(output))
	}

	return status.Errorf(codes.Internal, "failed to execute filesystem check for %s: %v", devicePath, err)
}

func runE2FSCK(ctx context.Context, devicePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Do not bind process lifetime to the RPC context. Killing e2fsck during a repair
	// can leave the filesystem in a worse state; let an in-progress check finish safely.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), e2fsckPath, "-p", devicePath)
	var output cappedCommandOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.Bytes(), err
}

type cappedCommandOutput struct {
	buffer bytes.Buffer
}

func (o *cappedCommandOutput) Write(p []byte) (int, error) {
	const captureLimit = 4097
	originalLength := len(p)
	if remaining := captureLimit - o.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = o.buffer.Write(p)
	}
	return originalLength, nil
}

func (o *cappedCommandOutput) Bytes() []byte {
	return o.buffer.Bytes()
}

func truncateCommandOutput(output []byte) string {
	const maxOutputBytes = 4096
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) <= maxOutputBytes {
		return trimmed
	}
	return trimmed[:maxOutputBytes] + "... (truncated)"
}
