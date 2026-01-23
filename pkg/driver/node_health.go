package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
)

// Static errors for health checks.
var (
	errMountTimeout      = errors.New("timeout checking mount status")
	errReadTimeout       = errors.New("timeout reading directory")
	errNotNVMeDevice     = errors.New("not an NVMe device")
	errISCSIStateUnknown = errors.New("could not determine iSCSI session state")
)

// VolumeHealth represents the health status of a volume.
type VolumeHealth struct {
	Message  string
	Abnormal bool
}

// Healthy returns a VolumeHealth indicating the volume is healthy.
func Healthy() VolumeHealth {
	return VolumeHealth{
		Abnormal: false,
		Message:  "",
	}
}

// Unhealthy returns a VolumeHealth indicating the volume is unhealthy.
func Unhealthy(message string) VolumeHealth {
	return VolumeHealth{
		Abnormal: true,
		Message:  message,
	}
}

// ToCSI converts VolumeHealth to a CSI VolumeCondition.
func (h VolumeHealth) ToCSI() *csi.VolumeCondition {
	return &csi.VolumeCondition{
		Abnormal: h.Abnormal,
		Message:  h.Message,
	}
}

// checkVolumeHealth checks the health of a volume based on its protocol.
// The stagingPath parameter is reserved for future use.
func (s *NodeService) checkVolumeHealth(ctx context.Context, volumePath, _ string) VolumeHealth {
	// Detect the protocol from the volume path
	protocol := s.detectProtocolFromVolumePath(ctx, volumePath)

	klog.V(4).Infof("Checking health for volume at %s (protocol: %s)", volumePath, protocol)

	switch protocol {
	case ProtocolNFS:
		return s.checkNFSHealth(ctx, volumePath)
	case ProtocolNVMeOF:
		return s.checkNVMeOFHealth(ctx, volumePath)
	case ProtocolISCSI:
		return s.checkISCSIHealth(ctx, volumePath)
	default:
		// Unknown protocol - just check if path is accessible
		return checkBasicHealth(volumePath)
	}
}

// detectProtocolFromVolumePath detects the protocol from the volume path.
func (s *NodeService) detectProtocolFromVolumePath(ctx context.Context, volumePath string) string {
	// Check the filesystem type using findmnt
	fsType, err := detectFilesystemType(ctx, volumePath)
	if err != nil {
		klog.V(4).Infof("Failed to detect filesystem type for %s: %v", volumePath, err)
		return ProtocolNFS // Default to NFS
	}

	// NFS mounts show "nfs" or "nfs4"
	if strings.HasPrefix(fsType, "nfs") {
		return ProtocolNFS
	}

	// For block device mounts, determine if NVMe-oF or iSCSI
	return s.detectBlockProtocolFromMount(ctx, volumePath)
}

// checkNFSHealth checks the health of an NFS mounted volume.
func (s *NodeService) checkNFSHealth(ctx context.Context, volumePath string) VolumeHealth {
	// Check 1: Verify the path exists and is accessible
	if _, err := os.Stat(volumePath); err != nil {
		if os.IsNotExist(err) {
			return Unhealthy("NFS mount path does not exist")
		}
		// Check for stale NFS handle
		if strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "Stale") {
			return Unhealthy("Stale NFS file handle")
		}
		return Unhealthy(fmt.Sprintf("NFS mount path not accessible: %v", err))
	}

	// Check 2: Verify it's still mounted
	mounted, err := isMountedWithTimeout(ctx, volumePath, 5*time.Second)
	if err != nil {
		return Unhealthy(fmt.Sprintf("Failed to check NFS mount status: %v", err))
	}
	if !mounted {
		return Unhealthy("NFS volume is not mounted")
	}

	// Check 3: Try to read the directory (detects stale handles that stat might miss)
	if err := checkDirectoryReadable(ctx, volumePath); err != nil {
		if strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "Stale") {
			return Unhealthy("Stale NFS file handle")
		}
		return Unhealthy(fmt.Sprintf("NFS mount not readable: %v", err))
	}

	return Healthy()
}

// checkNVMeOFHealth checks the health of an NVMe-oF volume.
func (s *NodeService) checkNVMeOFHealth(ctx context.Context, volumePath string) VolumeHealth {
	// Check 1: Verify the path exists
	if _, err := os.Stat(volumePath); err != nil {
		return Unhealthy(fmt.Sprintf("NVMe-oF volume path not accessible: %v", err))
	}

	// Check 2: Get the source device
	devicePath, err := getSourceDevice(ctx, volumePath)
	if err != nil {
		return Unhealthy(fmt.Sprintf("Failed to determine NVMe device: %v", err))
	}

	// Check 3: Verify the device exists
	if _, statErr := os.Stat(devicePath); statErr != nil {
		return Unhealthy(fmt.Sprintf("NVMe device %s not found", devicePath))
	}

	// Check 4: Check NVMe controller state
	ctrlState, err := getNVMeControllerState(devicePath)
	if err != nil {
		klog.V(4).Infof("Failed to get NVMe controller state: %v", err)
		// Don't fail health check if we can't read controller state
	} else if ctrlState != nvmeSubsystemStateLive {
		return Unhealthy(fmt.Sprintf("NVMe controller state is %q (expected: %s)", ctrlState, nvmeSubsystemStateLive))
	}

	return Healthy()
}

// checkISCSIHealth checks the health of an iSCSI volume.
func (s *NodeService) checkISCSIHealth(ctx context.Context, volumePath string) VolumeHealth {
	// Check 1: Verify the path exists
	if _, err := os.Stat(volumePath); err != nil {
		return Unhealthy(fmt.Sprintf("iSCSI volume path not accessible: %v", err))
	}

	// Check 2: Get the source device
	devicePath, err := getSourceDevice(ctx, volumePath)
	if err != nil {
		return Unhealthy(fmt.Sprintf("Failed to determine iSCSI device: %v", err))
	}

	// Check 3: Verify the device exists
	if _, statErr := os.Stat(devicePath); statErr != nil {
		return Unhealthy(fmt.Sprintf("iSCSI device %s not found", devicePath))
	}

	// Check 4: Check iSCSI session state
	sessionState, err := getISCSISessionState(ctx, devicePath)
	if err != nil {
		klog.V(4).Infof("Failed to get iSCSI session state: %v", err)
		// Don't fail health check if we can't read session state
	} else if sessionState != "LOGGED_IN" {
		return Unhealthy(fmt.Sprintf("iSCSI session state is %q (expected: LOGGED_IN)", sessionState))
	}

	return Healthy()
}

// checkBasicHealth performs basic health checks for unknown protocols.
func checkBasicHealth(volumePath string) VolumeHealth {
	if _, err := os.Stat(volumePath); err != nil {
		return Unhealthy(fmt.Sprintf("Volume path not accessible: %v", err))
	}
	return Healthy()
}

// isMountedWithTimeout checks if a path is mounted with a timeout.
func isMountedWithTimeout(ctx context.Context, path string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "findmnt", "-n", path)
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, errMountTimeout
	}
	if err != nil {
		// Exit code 1 means not mounted
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// checkDirectoryReadable attempts to read directory entries to verify mount is responsive.
func checkDirectoryReadable(ctx context.Context, path string) error {
	// Use a goroutine with timeout to prevent hanging on unresponsive mounts
	done := make(chan error, 1)
	go func() {
		f, err := os.Open(path) //nolint:gosec // path is from volume context, not user input
		if err != nil {
			done <- err
			return
		}
		// Try to read directory entries (just one is enough)
		_, err = f.Readdirnames(1)
		closeErr := f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			done <- err
			return
		}
		done <- closeErr
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errReadTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getSourceDevice gets the source device for a mount point.
func getSourceDevice(ctx context.Context, mountPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "findmnt", "-n", "-o", "SOURCE", mountPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("findmnt failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getNVMeControllerState reads the NVMe controller state from sysfs.
func getNVMeControllerState(devicePath string) (string, error) {
	// Device path is like /dev/nvme0n1 or /dev/nvme0n1p1
	// We need to extract the controller name (nvme0)
	base := filepath.Base(devicePath)
	if !strings.HasPrefix(base, "nvme") {
		return "", fmt.Errorf("%w: %s", errNotNVMeDevice, devicePath)
	}

	// Extract controller name (nvme0 from nvme0n1)
	var ctrlName string
	for i, c := range base {
		if c == 'n' && i > 4 { // Skip "nvme" prefix
			ctrlName = base[:i]
			break
		}
	}
	if ctrlName == "" {
		ctrlName = base // Fallback
	}

	// Read state from /sys/class/nvme/<ctrl>/state
	statePath := "/sys/class/nvme/" + ctrlName + "/state"
	data, err := os.ReadFile(statePath) //nolint:gosec // path is constructed from device name
	if err != nil {
		return "", fmt.Errorf("failed to read NVMe state: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// getISCSISessionState gets the state of an iSCSI session for a device.
func getISCSISessionState(ctx context.Context, devicePath string) (string, error) {
	// Find the session for this device by looking at /sys/block/<dev>/device/
	base := filepath.Base(devicePath)

	// For devices like /dev/sda, check /sys/block/sda/device/state
	statePath := "/sys/block/" + base + "/device/state"
	data, err := os.ReadFile(statePath) //nolint:gosec // path is constructed from device name
	if err == nil {
		state := strings.TrimSpace(string(data))
		// SCSI device states: running, blocked, quiesce, etc.
		if state == "running" {
			return "LOGGED_IN", nil
		}
		return state, nil
	}

	// Alternative: use iscsiadm to check session state
	cmd := exec.CommandContext(ctx, "iscsiadm", "-m", "session", "-P", "1")
	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		return "", fmt.Errorf("iscsiadm failed: %w", cmdErr)
	}

	// Parse output for session state
	// Look for "iSCSI Session State:" line
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "iSCSI Session State:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}

	return "", errISCSIStateUnknown
}
