package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	datasetName := volumeContext["datasetName"]
	namespaceID := volumeContext["nvmeofNamespaceID"]
	klog.Infof("Staging NVMe-oF volume %s (block mode: %v): server=%s:%s, NQN=%s, NSID=%s, dataset=%s, namespace=%s",
		volumeID, isBlockVolume, params.server, params.port, params.nqn, params.nsid, datasetName, namespaceID)

	// Check if already connected
	devicePath, err := s.findNVMeDeviceByNQNAndNSID(ctx, params.nqn, params.nsid)
	if err == nil && devicePath != "" {
		klog.Infof("NVMe-oF device already connected at %s (NQN: %s, NSID: %s, dataset: %s)",
			devicePath, params.nqn, params.nsid, datasetName)

		// CRITICAL: Flush device cache for "already connected" devices
		// When NVMe-oF disconnect/reconnect happens rapidly (e.g., during test runs or pod restarts),
		// the kernel may still have the device node present with STALE filesystem metadata from the
		// previous ZVOL. If TrueNAS reuses the same NSID for a new ZVOL, we might read old data!
		//
		// This happens because:
		// 1. Pod A uses ZVOL-1 on /dev/nvme0n2 (NSID=2)
		// 2. Pod A deleted, CSI calls "nvme disconnect"
		// 3. Kernel doesn't immediately remove /dev/nvme0n2, cached metadata remains
		// 4. TrueNAS creates ZVOL-2, reuses NSID=2
		// 5. Pod B connects, finds /dev/nvme0n2 "already connected"
		// 6. blkid reads STALE filesystem UUID from kernel cache -> wrong data!
		//
		// Solution: Force kernel to invalidate caches before proceeding
		klog.Infof("Flushing device cache for already-connected device %s to ensure fresh metadata", devicePath)
		flushCtx, flushCancel := context.WithTimeout(ctx, 5*time.Second)
		defer flushCancel()
		//nolint:gosec // blockdev with device path from validated NVMe discovery is safe
		flushCmd := exec.CommandContext(flushCtx, "blockdev", "--flushbufs", devicePath)
		if output, flushErr := flushCmd.CombinedOutput(); flushErr != nil {
			klog.Warningf("Failed to flush device buffers for already-connected device %s: %v, output: %s (continuing anyway)", devicePath, flushErr, string(output))
		} else {
			klog.V(4).Infof("Successfully flushed device buffers for already-connected device %s", devicePath)
		}

		// Wait for udev to settle after cache flush
		settleCtx, settleCancel := context.WithTimeout(ctx, 5*time.Second)
		defer settleCancel()
		settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=5")
		if settleOutput, settleErr := settleCmd.CombinedOutput(); settleErr != nil {
			klog.V(4).Infof("udevadm settle failed for already-connected device: %v, output: %s (continuing anyway)", settleErr, string(settleOutput))
		}

		// Register this namespace as active to prevent premature disconnect
		s.namespaceRegistry.Register(params.nqn, params.nsid)

		return s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
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
		return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after connection (NQN: %s, NSID: %s): %v",
			params.nqn, params.nsid, err)
	}

	klog.Infof("NVMe-oF device connected at %s (NQN: %s, NSID: %s, dataset: %s)",
		devicePath, params.nqn, params.nsid, datasetName)

	// Register this namespace as active to prevent premature disconnect
	s.namespaceRegistry.Register(params.nqn, params.nsid)

	return s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
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
func (s *NodeService) stageNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// CRITICAL: Wait for filesystem metadata to become available after device connection
	// When NVMe-oF devices connect (either fresh or reconnect), the kernel may not have
	// filesystem metadata immediately available. If we check too quickly with blkid,
	// it won't detect an existing ext4 filesystem, causing an erroneous reformat that
	// destroys user data. This is the same issue as snapshot clones but occurs during
	// normal pod restarts when devices reconnect.
	//
	// IMPORTANT: After forced pod deletion (grace-period=0), NVMe-oF devices may take
	// longer to stabilize because the previous connection was abruptly terminated.
	// We need to ensure the kernel has fully synchronized the device state before
	// checking for filesystems.
	//
	// For filesystem volumes, add a delay to ensure metadata is readable.
	if !isBlockVolume {
		const deviceMetadataDelay = 8 * time.Second
		klog.Infof("Waiting %v for device %s metadata to stabilize before filesystem check", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)
		klog.Infof("Device metadata stabilization delay complete for %s", devicePath)

		// Additionally flush device buffers to ensure kernel caches are clear
		// This is critical after reconnection to force re-reading of actual device state
		flushCtx, flushCancel := context.WithTimeout(ctx, 3*time.Second)
		defer flushCancel()
		flushCmd := exec.CommandContext(flushCtx, "blockdev", "--flushbufs", devicePath)
		if output, err := flushCmd.CombinedOutput(); err != nil {
			klog.Warningf("Failed to flush device buffers for %s: %v, output: %s", devicePath, err, string(output))
		} else {
			klog.V(4).Infof("Flushed device buffers for %s after connection", devicePath)
		}

		// Wait an additional moment after buffer flush for I/O subsystem to settle
		// This is especially important after forced pod termination where the previous
		// connection may have left the device in an inconsistent state
		const postFlushDelay = 2 * time.Second
		klog.V(4).Infof("Waiting %v after buffer flush for I/O subsystem to settle", postFlushDelay)
		time.Sleep(postFlushDelay)
	}

	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, volumeContext)
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

	// Get NSID from volume context first
	nsid := volumeContext["nsid"]

	// If NSID is not in volumeContext, decode it from volumeID
	// CSI spec doesn't guarantee volumeContext is passed to NodeUnstageVolume
	if nsid == "" {
		meta, err := decodeVolumeID(volumeID)
		if err != nil {
			klog.Errorf("Failed to get NSID for volume %s: cannot decode volumeID: %v", volumeID, err)
			klog.Errorf("Cannot safely disconnect - NSID required to avoid affecting other volumes sharing NQN=%s", nqn)
			return nil, status.Errorf(codes.Internal, "Cannot determine NSID for volume: %v", err)
		}
		nsid = strconv.Itoa(meta.NVMeOFNamespaceID)
		klog.Infof("Decoded NSID=%s from volumeID for volume %s", nsid, volumeID)
	}

	// Disconnect from NVMe-oF target ONLY if this is the last namespace for this NQN
	if nqn == "" {
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// Unregister namespace and check if we should disconnect
	isLastNamespace := s.namespaceRegistry.Unregister(nqn, nsid)
	if !isLastNamespace {
		activeCount := s.namespaceRegistry.GetNQNCount(nqn)
		klog.Infof("Unstaging volume %s: Skipping disconnect for NQN=%s (NSID=%s) - still has %d active namespace(s)",
			volumeID, nqn, nsid, activeCount)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// This is the last namespace, proceed with disconnect
	klog.Infof("Unstaging volume %s: Last namespace (NSID=%s) for NQN=%s, proceeding with disconnect",
		volumeID, nsid, nqn)
	if err := s.disconnectNVMeOF(ctx, nqn); err != nil {
		klog.Warningf("Failed to disconnect NVMe-oF device (continuing anyway): %v", err)
	} else {
		klog.Infof("Successfully disconnected from NVMe-oF target: %s", nqn)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// formatAndMountNVMeDevice formats (if needed) and mounts an NVMe device.
func (s *NodeService) formatAndMountNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	nsid := volumeContext["nsid"]
	nqn := volumeContext["nqn"]
	klog.Infof("Formatting and mounting NVMe device: device=%s, path=%s, volume=%s, dataset=%s, NQN=%s, NSID=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, nqn, nsid)

	// Log device information for troubleshooting
	s.logDeviceInfo(ctx, devicePath)

	// Determine filesystem type from volume capability
	fsType := "ext4" // default
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// CRITICAL: Check if this volume was cloned from a snapshot
	// If clonedFromSnapshot flag is set, SKIP formatting check entirely to preserve data
	// ZFS clones inherit the filesystem from the snapshot, but the filesystem may not be
	// immediately detectable by blkid due to kernel caching or metadata sync delays.
	// Formatting a cloned volume would DESTROY the snapshot data.
	if cloned, exists := volumeContext["clonedFromSnapshot"]; exists && cloned == "true" {
		klog.Warningf("Volume %s was cloned from snapshot - SKIPPING format check to preserve data", volumeID)
		klog.Infof("Device %s is a cloned snapshot volume, assuming existing filesystem", devicePath)
	} else if err := s.handleDeviceFormatting(ctx, volumeID, devicePath, fsType, datasetName, nqn, nsid); err != nil {
		return nil, err
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
	klog.V(4).Infof("Searching for NVMe device: NQN=%s, NSID=%s", nqn, nsid)

	// Use nvme list-subsys which shows NQN
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	subsysOutput, err := subsysCmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, falling back to sysfs", err)
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
							klog.V(4).Infof("Found NVMe device from list-subsys: %s (controller: %s, NQN: %s, NSID: %s)",
								devicePath, controllerName, nqn, nsid)
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
	klog.V(4).Infof("Searching for NVMe device via sysfs: NQN=%s, NSID=%s", nqn, nsid)

	// Read /sys/class/nvme/nvmeX/subsysnqn for each device
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", nvmeDir, err)
	}

	klog.V(4).Infof("Found %d NVMe controller(s) in sysfs", len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		deviceName := entry.Name()
		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")

		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(5).Infof("Cannot read NQN for %s: %v", deviceName, err)
			continue
		}

		deviceNQN := strings.TrimSpace(string(data))
		if deviceNQN == nqn {
			// Found the device, construct path with provided nsid
			// Typically nvme0 + nsid 2 -> /dev/nvme0n2
			devicePath := fmt.Sprintf("/dev/%sn%s", deviceName, nsid)
			if _, err := os.Stat(devicePath); err == nil {
				klog.V(4).Infof("Found NVMe device from sysfs: %s (controller: %s, NQN: %s, NSID: %s)",
					devicePath, deviceName, nqn, nsid)
				return devicePath, nil
			}
			klog.Warningf("Found matching NQN on %s but device path %s does not exist", deviceName, devicePath)
		} else {
			klog.V(5).Infof("Controller %s has different NQN: %s (looking for: %s)", deviceName, deviceNQN, nqn)
		}
	}

	klog.Warningf("NVMe device not found in sysfs for NQN=%s, NSID=%s", nqn, nsid)
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

// handleDeviceFormatting checks if a device needs formatting and formats it if necessary.
func (s *NodeService) handleDeviceFormatting(ctx context.Context, volumeID, devicePath, fsType, datasetName, nqn, nsid string) error {
	// Check if device is already formatted (only for non-cloned volumes)
	needsFormat, err := needsFormat(ctx, devicePath)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if needsFormat {
		klog.Infof("Device %s needs formatting with %s (dataset: %s)", devicePath, fsType, datasetName)
		if formatErr := formatDevice(ctx, volumeID, devicePath, fsType); formatErr != nil {
			return status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}
		return nil
	}

	klog.Infof("Device %s is already formatted, preserving existing filesystem (dataset: %s, NQN: %s, NSID: %s)",
		devicePath, datasetName, nqn, nsid)
	return nil
}

// logDeviceInfo logs detailed information about an NVMe device for troubleshooting.
func (s *NodeService) logDeviceInfo(ctx context.Context, devicePath string) {
	// Log basic device info
	if stat, err := os.Stat(devicePath); err == nil {
		klog.V(4).Infof("Device %s: exists, size=%d bytes", devicePath, stat.Size())
	} else {
		klog.Warningf("Device %s: stat failed: %v", devicePath, err)
		return
	}

	// Try to get device UUID (for better tracking)
	uuidCtx, uuidCancel := context.WithTimeout(ctx, 3*time.Second)
	defer uuidCancel()
	blkidCmd := exec.CommandContext(uuidCtx, "blkid", "-s", "UUID", "-o", "value", devicePath)
	if uuidOutput, err := blkidCmd.CombinedOutput(); err == nil && len(uuidOutput) > 0 {
		uuid := strings.TrimSpace(string(uuidOutput))
		if uuid != "" {
			klog.Infof("Device %s has filesystem UUID: %s", devicePath, uuid)
		}
	}

	// Try to get filesystem type
	fsTypeCtx, fsTypeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer fsTypeCancel()
	fsCmd := exec.CommandContext(fsTypeCtx, "blkid", "-s", "TYPE", "-o", "value", devicePath)
	if fsOutput, err := fsCmd.CombinedOutput(); err == nil && len(fsOutput) > 0 {
		fsType := strings.TrimSpace(string(fsOutput))
		if fsType != "" {
			klog.Infof("Device %s has filesystem type: %s", devicePath, fsType)
		}
	}
}

// disconnectNVMeOF disconnects from an NVMe-oF target and waits for device cleanup.
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

	// CRITICAL: Wait for kernel to actually remove the device nodes
	// Without this wait, rapid disconnect/reconnect cycles (common in tests and pod restarts)
	// can lead to the "already connected" path finding stale devices with old filesystem metadata.
	// Give the kernel a moment to clean up device nodes to prevent NSID reuse issues.
	const deviceCleanupDelay = 2 * time.Second
	klog.V(4).Infof("Waiting %v for kernel to cleanup NVMe devices after disconnect", deviceCleanupDelay)
	select {
	case <-time.After(deviceCleanupDelay):
		klog.V(4).Infof("Device cleanup delay complete")
	case <-ctx.Done():
		klog.Warningf("Context canceled during device cleanup delay: %v", ctx.Err())
		return ctx.Err()
	}

	return nil
}
