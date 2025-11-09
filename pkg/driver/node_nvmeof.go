package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for NVMe-oF operations.
var (
	ErrNVMeCLINotFound    = errors.New("nvme command not found - please install nvme-cli")
	ErrNVMeDeviceNotFound = errors.New("NVMe device not found")
	ErrNVMeDeviceTimeout  = errors.New("timeout waiting for NVMe device to appear")
)

// nvmeOFConnectionParams holds validated NVMe-oF connection parameters.
type nvmeOFConnectionParams struct {
	nqn       string
	server    string
	transport string
	port      string
	nsid      string
}

// stageNVMeOFVolume stages an NVMe-oF volume by connecting to the target.
func (s *NodeService) stageNVMeOFVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Validate and extract connection parameters
	params, err := s.validateNVMeOFParams(volumeContext)
	if err != nil {
		return nil, err
	}

	isBlockVolume := volumeCapability.GetBlock() != nil
	klog.Infof("Staging NVMe-oF volume %s (block mode: %v): connecting to %s:%s (NQN: %s, NSID: %s)",
		volumeID, isBlockVolume, params.server, params.port, params.nqn, params.nsid)

	// Check if already connected
	devicePath, err := s.findNVMeDeviceByNQNAndNSID(ctx, params.nqn, params.nsid)
	if err == nil && devicePath != "" {
		klog.Infof("NVMe-oF device already connected at %s", devicePath)
		return s.stageNVMeDevice(ctx, devicePath, stagingTargetPath, volumeCapability, isBlockVolume)
	}

	// Check if nvme-cli is installed
	if checkErr := s.checkNVMeCLI(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "nvme-cli not available: %v", checkErr)
	}

	// Connect to NVMe-oF target
	if connectErr := s.connectNVMeOFTarget(ctx, params); connectErr != nil {
		return nil, connectErr
	}

	// Wait for device to appear
	devicePath, err = s.waitForNVMeDevice(ctx, params.nqn, params.nsid, 30*time.Second)
	if err != nil {
		// Cleanup: disconnect on failure
		if disconnectErr := s.disconnectNVMeOF(ctx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect NVMe-oF after device wait failure: %v", disconnectErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after connection: %v", err)
	}

	klog.Infof("NVMe-oF device connected at %s", devicePath)
	return s.stageNVMeDevice(ctx, devicePath, stagingTargetPath, volumeCapability, isBlockVolume)
}

// validateNVMeOFParams validates and extracts NVMe-oF connection parameters from volume context.
func (s *NodeService) validateNVMeOFParams(volumeContext map[string]string) (*nvmeOFConnectionParams, error) {
	params := &nvmeOFConnectionParams{
		nqn:       volumeContext["nqn"],
		server:    volumeContext["server"],
		transport: volumeContext["transport"],
		port:      volumeContext["port"],
		nsid:      volumeContext["nsid"],
	}

	if params.nqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "nqn and server must be provided in volume context for NVMe-oF volumes")
	}

	if params.nsid == "" {
		return nil, status.Error(codes.InvalidArgument, "nsid (namespace ID) must be provided in volume context for NVMe-oF volumes")
	}

	// Default values
	if params.transport == "" {
		params.transport = "tcp"
	}
	if params.port == "" {
		params.port = "4420"
	}

	return params, nil
}

// connectNVMeOFTarget discovers and connects to an NVMe-oF target.
func (s *NodeService) connectNVMeOFTarget(ctx context.Context, params *nvmeOFConnectionParams) error {
	// Discover the NVMe-oF target
	klog.V(4).Infof("Discovering NVMe-oF target at %s:%s", params.server, params.port)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
	defer discoverCancel()
	//nolint:gosec // nvme discover with volume context variables is expected for CSI driver
	discoverCmd := exec.CommandContext(discoverCtx, "nvme", "discover", "-t", params.transport, "-a", params.server, "-s", params.port)
	if output, discoverErr := discoverCmd.CombinedOutput(); discoverErr != nil {
		klog.Warningf("NVMe discover failed (this may be OK if target is already known): %v, output: %s", discoverErr, string(output))
	}

	// Connect to the NVMe-oF target
	klog.Infof("Connecting to NVMe-oF target: %s", params.nqn)
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connectCancel()
	//nolint:gosec // nvme connect with volume context variables is expected for CSI driver
	connectCmd := exec.CommandContext(connectCtx, "nvme", "connect", "-t", params.transport, "-n", params.nqn, "-a", params.server, "-s", params.port)
	output, err := connectCmd.CombinedOutput()
	if err != nil {
		// Check if already connected
		if strings.Contains(string(output), "already connected") {
			klog.V(4).Infof("NVMe device already connected (output: %s)", string(output))
			return nil
		}
		return status.Errorf(codes.Internal, "Failed to connect to NVMe-oF target: %v, output: %s", err, string(output))
	}

	return nil
}

// stageNVMeDevice stages an NVMe device as either block or filesystem volume.
func (s *NodeService) stageNVMeDevice(ctx context.Context, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool) (*csi.NodeStageVolumeResponse, error) {
	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountNVMeDevice(ctx, devicePath, stagingTargetPath, volumeCapability)
}

// unstageNVMeOFVolume unstages an NVMe-oF volume by disconnecting from the target.
func (s *NodeService) unstageNVMeOFVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.Infof("Unstaging NVMe-oF volume %s from %s", volumeID, stagingTargetPath)

	// Get NQN from volume context
	nqn := volumeContext["nqn"]
	if nqn == "" {
		// Try to decode from volumeID
		meta, err := decodeVolumeID(volumeID)
		if err != nil {
			klog.Warningf("Failed to get NQN for volume %s: %v", volumeID, err)
			return &csi.NodeUnstageVolumeResponse{}, nil
		}
		nqn = meta.NVMeOFNQN
	}

	// Check if mounted and unmount if necessary
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.Infof("Unmounting staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
		}
	}

	// Disconnect from NVMe-oF target
	if nqn != "" {
		if err := s.disconnectNVMeOF(ctx, nqn); err != nil {
			klog.Warningf("Failed to disconnect NVMe-oF device (continuing anyway): %v", err)
		} else {
			klog.Infof("Successfully disconnected from NVMe-oF target: %s", nqn)
		}
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// formatAndMountNVMeDevice formats (if needed) and mounts an NVMe device.
func (s *NodeService) formatAndMountNVMeDevice(ctx context.Context, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability) (*csi.NodeStageVolumeResponse, error) {
	klog.Infof("Formatting and mounting NVMe device %s to %s", devicePath, stagingTargetPath)

	// Determine filesystem type from volume capability
	fsType := "ext4" // default
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Wait for device to be fully ready
	if err := s.waitForDeviceReady(ctx, devicePath); err != nil {
		return nil, status.Errorf(codes.Internal, "Device not ready: %v", err)
	}

	// Check if device is already formatted
	needsFormat, err := needsFormat(ctx, devicePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if needsFormat {
		klog.Infof("Formatting device %s with filesystem %s", devicePath, fsType)

		if formatErr := formatDevice(ctx, devicePath, fsType); formatErr != nil {
			return nil, status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}
	} else {
		klog.V(4).Infof("Device %s is already formatted", devicePath)
	}

	// Create staging target path if it doesn't exist
	if mkdirErr := os.MkdirAll(stagingTargetPath, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", mkdirErr)
	}

	// Check if already mounted
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Mount the device
	klog.Infof("Mounting device %s to %s", devicePath, stagingTargetPath)
	mountOptions := []string{}
	if mnt := volumeCapability.GetMount(); mnt != nil {
		mountOptions = mnt.MountFlags
	}

	args := []string{devicePath, stagingTargetPath}
	if len(mountOptions) > 0 {
		args = []string{"-o", mount.JoinMountOptions(mountOptions), devicePath, stagingTargetPath}
	}

	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	//nolint:gosec // mount command with dynamic args is expected for CSI driver
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully mounted NVMe device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

// checkNVMeCLI checks if nvme-cli is installed.
func (s *NodeService) checkNVMeCLI(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "nvme", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrNVMeCLINotFound, err)
	}
	return nil
}

// findNVMeDeviceByNQNAndNSID finds the device path for a given NQN and namespace ID.
//
//nolint:gocognit // Complex NVMe device discovery - refactoring would risk stability of working code
func (s *NodeService) findNVMeDeviceByNQNAndNSID(ctx context.Context, nqn, nsid string) (string, error) {
	// Use nvme list-subsys which shows NQN
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	subsysOutput, err := subsysCmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v", err)
		// Fall back to checking /sys/class/nvme
		return s.findNVMeDeviceByNQNAndNSIDFromSys(ctx, nqn, nsid)
	}

	// Parse output to find NQN and extract controller name
	// The JSON format from nvme list-subsys has: "Name" : "nvmeX" under Paths
	// We need to construct the device path as /dev/nvmeXn{nsid}
	lines := strings.Split(string(subsysOutput), "\n")
	foundNQN := false
	for i, line := range lines {
		// Look for the NQN line
		if strings.Contains(line, nqn) {
			foundNQN = true
			// Now look ahead for the "Name" field in the Paths section
			for j := i; j < len(lines) && j < i+20; j++ {
				if strings.Contains(lines[j], "\"Name\"") && strings.Contains(lines[j], "nvme") {
					// Extract controller name - format: "Name" : "nvme0"
					parts := strings.Split(lines[j], "\"")
					for k := range len(parts) - 1 {
						if parts[k] == "Name" && k+2 < len(parts) {
							controllerName := strings.TrimSpace(parts[k+2])
							// Construct device path using provided nsid - typically nvme0 + nsid 2 -> /dev/nvme0n2
							devicePath := fmt.Sprintf("/dev/%sn%s", controllerName, nsid)
							return devicePath, nil
						}
					}
				}
			}
		}
	}

	if foundNQN {
		klog.Warningf("Found NQN but could not extract device name, falling back to sysfs")
	}

	// Fall back to checking /sys/class/nvme if JSON parsing failed
	return s.findNVMeDeviceByNQNAndNSIDFromSys(ctx, nqn, nsid)
}

// findNVMeDeviceByNQNAndNSIDFromSys finds NVMe device by checking /sys/class/nvme.
func (s *NodeService) findNVMeDeviceByNQNAndNSIDFromSys(ctx context.Context, nqn, nsid string) (string, error) {
	_ = ctx // Reserved for future cancellation support
	// Read /sys/class/nvme/nvmeX/subsysnqn for each device
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", nvmeDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		deviceName := entry.Name()
		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")

		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			continue
		}

		if strings.TrimSpace(string(data)) == nqn {
			// Found the device, construct path with provided nsid
			// Typically nvme0 + nsid 2 -> /dev/nvme0n2
			devicePath := fmt.Sprintf("/dev/%sn%s", deviceName, nsid)
			if _, err := os.Stat(devicePath); err == nil {
				return devicePath, nil
			}
		}
	}

	return "", fmt.Errorf("%w for NQN: %s NSID: %s", ErrNVMeDeviceNotFound, nqn, nsid)
}

// waitForNVMeDevice waits for the NVMe device to appear after connection.
func (s *NodeService) waitForNVMeDevice(ctx context.Context, nqn, nsid string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		devicePath, err := s.findNVMeDeviceByNQNAndNSID(ctx, nqn, nsid)
		if err == nil && devicePath != "" {
			// Verify device is accessible
			if _, err := os.Stat(devicePath); err == nil {
				klog.Infof("NVMe device found at %s after %d attempts", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("%w after %d attempts", ErrNVMeDeviceTimeout, attempt)
}

// waitForDeviceReady waits for a device to be fully ready for I/O operations.
// This uses udevadm settle (if available) and polls for device accessibility.
func (s *NodeService) waitForDeviceReady(ctx context.Context, devicePath string) error {
	klog.V(4).Infof("Waiting for device %s to be ready", devicePath)

	// First, try to use udevadm settle to wait for udev processing
	// This is the proper way to wait for device initialization on Linux
	settleCtx, settleCancel := context.WithTimeout(ctx, 10*time.Second)
	defer settleCancel()

	// Run udevadm settle with a timeout
	// This waits for udev to process all events related to device initialization
	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "-t", "10")
	if output, err := settleCmd.CombinedOutput(); err != nil {
		// udevadm might not be available or might fail - log warning but continue
		klog.V(4).Infof("udevadm settle failed (this may be OK): %v, output: %s", err, string(output))
	} else {
		klog.V(4).Infof("udevadm settle completed successfully")
	}

	// Additional polling to verify device is accessible and ready
	// Some platforms may need extra time even after udevadm settle
	deadline := time.Now().Add(10 * time.Second)
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++

		// Check if device exists
		info, err := os.Stat(devicePath)
		if err != nil {
			klog.V(4).Infof("Device stat attempt %d failed: %v", attempt, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Verify it's a device or symlink (not a regular file/directory)
		mode := info.Mode()
		if mode&os.ModeDevice == 0 && mode&os.ModeSymlink == 0 {
			klog.V(4).Infof("Path exists but is not a device (mode: %v), attempt %d", mode, attempt)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Try to read device size using blockdev - this verifies the device is operational
		sizeCtx, sizeCancel := context.WithTimeout(ctx, 2*time.Second)
		sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
		output, sizeErr := sizeCmd.CombinedOutput()
		sizeCancel()

		if sizeErr != nil {
			klog.V(4).Infof("blockdev check attempt %d failed: %v, output: %s", attempt, sizeErr, string(output))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Device is accessible and operational
		klog.Infof("Device %s is ready after %d attempts (size: %s bytes)", devicePath, attempt, strings.TrimSpace(string(output)))
		return nil
	}

	return fmt.Errorf("timeout waiting for device %s to be ready after %d attempts", devicePath, attempt)
}

// disconnectNVMeOF disconnects from an NVMe-oF target.
func (s *NodeService) disconnectNVMeOF(ctx context.Context, nqn string) error {
	klog.V(4).Infof("Disconnecting from NVMe-oF target: %s", nqn)

	disconnectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(disconnectCtx, "nvme", "disconnect", "-n", nqn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already disconnected
		if strings.Contains(string(output), "No subsystems") || strings.Contains(string(output), "not found") {
			klog.V(4).Infof("NVMe device already disconnected")
			return nil
		}
		return fmt.Errorf("failed to disconnect NVMe-oF device: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully disconnected from NVMe-oF target")
	return nil
}
