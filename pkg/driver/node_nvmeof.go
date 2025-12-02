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
	ErrNVMeCLINotFound             = errors.New("nvme command not found - please install nvme-cli")
	ErrNVMeDeviceNotFound          = errors.New("NVMe device not found")
	ErrNVMeDeviceTimeout           = errors.New("timeout waiting for NVMe device to appear")
	ErrDeviceInitializationTimeout = errors.New("device failed to initialize - size remained zero or unreadable")
	ErrNVMeControllerNotFound      = errors.New("could not extract NVMe controller path from device path")
	ErrDeviceSizeMismatch          = errors.New("device size does not match expected capacity - possible NSID reuse")
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
	klog.V(4).Infof("Staging NVMe-oF volume %s (block mode: %v): server=%s:%s, NQN=%s, NSID=%s, dataset=%s, namespace=%s",
		volumeID, isBlockVolume, params.server, params.port, params.nqn, params.nsid, datasetName, namespaceID)

	// Try to reuse existing connection (idempotent staging)
	if resp, _, reuseErr := s.tryReuseExistingConnection(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext); reuseErr != nil {
		return nil, reuseErr
	} else if resp != nil {
		return resp, nil
	}

	// Check if nvme-cli is installed
	if checkErr := s.checkNVMeCLI(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "nvme-cli not available: %v", checkErr)
	}

	// Try to discover device via controller rescan (for newly created namespaces)
	if resp := s.tryDiscoverViaControllerRescan(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext); resp != nil {
		return resp, nil
	}

	// Connect to NVMe-oF target and stage device
	return s.connectAndStageDevice(ctx, params, volumeID, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext, datasetName)
}

// tryReuseExistingConnection attempts to reuse an existing NVMe-oF connection.
// Returns the response if successful, or nil if no existing connection found.
// CRITICAL: Following the pattern used by democratic-csi and iSCSI CSI drivers:
// If the device is already connected, REUSE the existing connection instead of
// disconnecting and reconnecting. Forced disconnect/reconnect was causing data loss
// because it confuses the kernel's device state.
func (s *NodeService) tryReuseExistingConnection(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (resp *csi.NodeStageVolumeResponse, devicePath string, err error) {
	devicePath, findErr := s.findNVMeDeviceByNQNAndNSID(ctx, params.nqn, params.nsid)
	if findErr != nil || devicePath == "" {
		// Device not found is expected when not previously connected - return nil to try other methods
		return nil, "", nil //nolint:nilerr // intentionally swallowing "device not found" as this is expected
	}

	klog.V(4).Infof("NVMe-oF device already connected at %s for NQN=%s NSID=%s - reusing existing connection (idempotent)",
		devicePath, params.nqn, params.nsid)

	// Rescan the namespace to ensure we have fresh data from the target
	// This is what democratic-csi does to ensure the kernel has current device state
	if rescanErr := s.rescanNVMeNamespace(ctx, devicePath); rescanErr != nil {
		klog.Warningf("Failed to rescan NVMe namespace %s: %v (continuing anyway)", devicePath, rescanErr)
	}

	// Register this namespace as active to prevent premature disconnect
	s.namespaceRegistry.Register(params.nqn, params.nsid)
	// Also register volume metadata for lookup during unstage
	s.namespaceRegistry.RegisterVolume(volumeID, params.nqn, params.nsid)

	// Proceed directly to staging with the existing device
	resp, err = s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	if err != nil {
		klog.Errorf("Failed to stage existing NVMe device: %v", err)
		return nil, devicePath, err
	}
	return resp, devicePath, nil
}

// tryDiscoverViaControllerRescan attempts to discover a namespace by rescanning an existing controller.
// This handles the case where new namespaces are created on an already-connected subsystem
// (e.g., snapshot restore creates a new NSID while original volume is still mounted).
func (s *NodeService) tryDiscoverViaControllerRescan(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) *csi.NodeStageVolumeResponse {
	existingController := s.findControllerByNQN(ctx, params.nqn)
	if existingController == "" {
		return nil
	}

	klog.V(4).Infof("Subsystem %s already connected via controller %s, rescanning to discover new namespace NSID=%s",
		params.nqn, existingController, params.nsid)

	// Rescan the controller to discover new namespaces
	if rescanErr := s.rescanNVMeController(ctx, existingController); rescanErr != nil {
		klog.Warningf("Failed to rescan controller %s: %v (will try connect anyway)", existingController, rescanErr)
		return nil
	}

	// Wait for kernel to process the rescan
	// This needs more time for cloned ZVOLs where the namespace was just created
	// and the kernel needs to discover and initialize the new device
	time.Sleep(5 * time.Second)

	// Try to find the device again after rescan
	// Use longer timeout for rescanned namespaces - cloned volumes may take
	// additional time for filesystem metadata to propagate through NVMe-oF layers
	devicePath, err := s.waitForNVMeDevice(ctx, params.nqn, params.nsid, 30*time.Second)
	if err != nil || devicePath == "" {
		klog.Warningf("Device not found after rescan, will try full connect: %v", err)
		return nil
	}

	klog.V(4).Infof("Found new namespace device %s after controller rescan", devicePath)

	// Register this namespace as active to prevent premature disconnect
	s.namespaceRegistry.Register(params.nqn, params.nsid)
	// Also register volume metadata for lookup during unstage
	s.namespaceRegistry.RegisterVolume(volumeID, params.nqn, params.nsid)

	resp, err := s.stageNVMeDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	if err != nil {
		klog.Errorf("Failed to stage NVMe device after controller rescan: %v", err)
	}
	return resp
}

// connectAndStageDevice connects to the NVMe-oF target and stages the device.
func (s *NodeService) connectAndStageDevice(ctx context.Context, params *nvmeOFConnectionParams, volumeID, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string, datasetName string) (*csi.NodeStageVolumeResponse, error) {
	// Connect to NVMe-oF target (this handles both new connections and retries)
	if connectErr := s.connectNVMeOFTarget(ctx, params); connectErr != nil {
		return nil, connectErr
	}

	// Wait for device to appear
	devicePath, err := s.waitForNVMeDevice(ctx, params.nqn, params.nsid, 30*time.Second)
	if err != nil {
		// Cleanup: disconnect on failure
		if disconnectErr := s.disconnectNVMeOF(ctx, params.nqn); disconnectErr != nil {
			klog.Warningf("Failed to disconnect NVMe-oF after device wait failure: %v", disconnectErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to find NVMe device after connection (NQN: %s, NSID: %s): %v",
			params.nqn, params.nsid, err)
	}

	klog.V(4).Infof("NVMe-oF device connected at %s (NQN: %s, NSID: %s, dataset: %s)",
		devicePath, params.nqn, params.nsid, datasetName)

	// Register this namespace as active to prevent premature disconnect
	s.namespaceRegistry.Register(params.nqn, params.nsid)
	// Also register volume metadata for lookup during unstage
	s.namespaceRegistry.RegisterVolume(volumeID, params.nqn, params.nsid)

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
	klog.V(4).Infof("Connecting to NVMe-oF target: %s", params.nqn)
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

// waitForDeviceInitialization waits for an NVMe device to be fully initialized.
// A device is considered initialized when it reports a non-zero size.
// This is critical to prevent checking filesystem metadata on a device that hasn't
// been fully initialized by the kernel yet, which can lead to false "no filesystem"
// detection and subsequent data-destroying reformatting.
func waitForDeviceInitialization(ctx context.Context, devicePath string) error {
	const (
		maxAttempts   = 30               // 30 attempts
		checkInterval = 1 * time.Second  // 1 second between checks
		totalTimeout  = 35 * time.Second // Maximum wait time
	)

	klog.V(4).Infof("Waiting for device %s to be fully initialized (non-zero size)", devicePath)

	// Create a context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	for attempt := range maxAttempts {
		// Check if context is canceled
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("%w for device %s: %w", ErrDeviceInitializationTimeout, devicePath, timeoutCtx.Err())
		default:
		}

		// Get device size using blockdev
		sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
		cmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
		output, err := cmd.CombinedOutput()
		sizeCancel()

		if err == nil {
			sizeStr := strings.TrimSpace(string(output))
			if size, parseErr := strconv.ParseInt(sizeStr, 10, 64); parseErr == nil && size > 0 {
				klog.V(4).Infof("Device %s initialized successfully with size %d bytes (after %d attempts)", devicePath, size, attempt+1)
				return nil
			}
			klog.V(4).Infof("Device %s size check attempt %d/%d: size=%s (waiting for non-zero)", devicePath, attempt+1, maxAttempts, sizeStr)
		} else {
			klog.V(4).Infof("Device %s size check attempt %d/%d failed: %v (device may not be ready yet)", devicePath, attempt+1, maxAttempts, err)
		}

		// Wait before next attempt (unless this is the last attempt)
		if attempt < maxAttempts-1 {
			select {
			case <-time.After(checkInterval):
			case <-timeoutCtx.Done():
				return fmt.Errorf("%w for device %s: %w", ErrDeviceInitializationTimeout, devicePath, timeoutCtx.Err())
			}
		}
	}

	return ErrDeviceInitializationTimeout
}

// findControllerByNQN finds an NVMe controller that is connected to the given NQN.
// Returns the controller path (e.g., "/dev/nvme0") if found, empty string otherwise.
// This is used to detect when a subsystem is already connected but a specific
// namespace isn't visible yet (e.g., newly created namespace from snapshot restore).
func (s *NodeService) findControllerByNQN(ctx context.Context, nqn string) string {
	_ = ctx // Reserved for future cancellation support
	klog.V(4).Infof("Searching for existing controller connected to NQN: %s", nqn)

	// Read /sys/class/nvme/nvmeX/subsysnqn for each controller
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		klog.V(4).Infof("Failed to read %s: %v", nvmeDir, err)
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		deviceName := entry.Name()
		// Skip non-controller entries (like "ctl", "nvme-subsys0", etc.)
		// Controller entries are like "nvme0", "nvme1", etc.
		if !strings.HasPrefix(deviceName, "nvme") {
			continue
		}
		// Skip if it contains additional segments (namespace devices, subsystem refs)
		if strings.Contains(deviceName[4:], "n") || strings.Contains(deviceName, "-") {
			continue
		}

		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")
		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(5).Infof("Cannot read NQN for %s: %v", deviceName, err)
			continue
		}

		deviceNQN := strings.TrimSpace(string(data))
		if deviceNQN == nqn {
			controllerPath := "/dev/" + deviceName
			klog.V(4).Infof("Found controller %s connected to NQN %s", controllerPath, nqn)
			return controllerPath
		}
	}

	klog.V(4).Infof("No existing controller found for NQN: %s", nqn)
	return ""
}

// rescanNVMeController rescans an NVMe controller to discover new namespaces.
// This is essential when new namespaces are added to an already-connected subsystem
// (e.g., when restoring from snapshot while the original volume is still mounted).
// The nvme ns-rescan command forces the kernel to re-query the target for namespace changes.
func (s *NodeService) rescanNVMeController(ctx context.Context, controllerPath string) error {
	klog.V(4).Infof("Rescanning NVMe controller %s to discover new namespaces", controllerPath)

	rescanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// nvme ns-rescan forces the kernel to re-read namespace list from the target
	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Warningf("nvme ns-rescan failed for %s: %v, output: %s", controllerPath, err, string(output))
		return fmt.Errorf("ns-rescan failed for %s: %w, output: %s", controllerPath, err, string(output))
	}

	klog.V(4).Infof("Successfully rescanned NVMe controller %s", controllerPath)
	return nil
}

// stageNVMeDevice stages an NVMe device as either block or filesystem volume.
func (s *NodeService) stageNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// CRITICAL: Wait for device to be fully initialized before proceeding
	// When NVMe-oF devices connect (either fresh or reconnect), the kernel may not have
	// the device fully initialized immediately. The device may appear in /dev but report
	// 0 bytes in size, and filesystem metadata won't be readable by blkid/lsblk.
	// If we check too quickly, it won't detect an existing ext4 filesystem, causing an
	// erroneous reformat that destroys user data.
	//
	// IMPORTANT: After forced pod deletion (grace-period=0), NVMe-oF devices may take
	// longer to stabilize because the previous connection was abruptly terminated.
	// We need to ensure the kernel has fully synchronized the device state before
	// checking for filesystems.
	//
	// For filesystem volumes, wait for device to be fully initialized.
	if !isBlockVolume {
		// First, wait for device to report non-zero size (indicates device is initialized)
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			return nil, status.Errorf(codes.Internal, "Device initialization timeout: %v", err)
		}

		// CRITICAL: Force the kernel to completely re-read the device identity
		// This is essential to handle NSID reuse by TrueNAS. When a namespace is deleted
		// and a new one is created with the same NSID, the kernel may cache stale metadata
		// from the previous ZVOL. Simply flushing buffers is NOT enough - we need to force
		// a complete re-read of the device's block layer identity.
		if err := forceDeviceRescan(ctx, devicePath); err != nil {
			klog.Warningf("Device rescan warning for %s: %v (continuing anyway)", devicePath, err)
		}

		// Additional stabilization delay to ensure metadata is readable after rescan
		const deviceMetadataDelay = 3 * time.Second
		klog.V(4).Infof("Waiting %v for device %s metadata to stabilize after rescan", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)
		klog.V(4).Infof("Device metadata stabilization delay complete for %s", devicePath)
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

	klog.V(4).Infof("Unstaging NVMe-oF volume %s from %s", volumeID, stagingTargetPath)

	// Get NQN and NSID from volume context first
	nqn := volumeContext["nqn"]
	nsid := volumeContext["nsid"]

	// If not in volumeContext, try to get from namespace registry (stored during stage)
	if nqn == "" || nsid == "" {
		if meta, found := s.namespaceRegistry.GetVolumeMetadata(volumeID); found {
			if nqn == "" {
				nqn = meta.NQN
			}
			if nsid == "" {
				nsid = meta.NSID
			}
			klog.V(4).Infof("Retrieved NVMe-oF metadata from registry: volumeID=%s, NQN=%s, NSID=%s", volumeID, nqn, nsid)
		} else {
			klog.Warningf("NVMe-oF metadata not found in registry for volume %s", volumeID)
		}
	}

	// Check if mounted and unmount if necessary
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
		}
	}

	// If we still don't have NQN or NSID, we can't safely disconnect
	if nqn == "" || nsid == "" {
		klog.Warningf("Cannot determine NQN/NSID for volume %s - skipping NVMe-oF disconnect", volumeID)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// Unregister namespace and check if we should disconnect
	isLastNamespace := s.namespaceRegistry.Unregister(nqn, nsid)
	// Also unregister the volume metadata
	s.namespaceRegistry.UnregisterVolume(volumeID)

	if !isLastNamespace {
		activeCount := s.namespaceRegistry.NQNCount(nqn)
		klog.V(4).Infof("Unstaging volume %s: Skipping disconnect for NQN=%s (NSID=%s) - still has %d active namespace(s)",
			volumeID, nqn, nsid, activeCount)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// This is the last namespace, proceed with disconnect
	klog.V(4).Infof("Unstaging volume %s: Last namespace (NSID=%s) for NQN=%s, proceeding with disconnect",
		volumeID, nsid, nqn)
	if err := s.disconnectNVMeOF(ctx, nqn); err != nil {
		klog.Warningf("Failed to disconnect NVMe-oF device (continuing anyway): %v", err)
	} else {
		klog.V(4).Infof("Disconnected from NVMe-oF target: %s", nqn)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// formatAndMountNVMeDevice formats (if needed) and mounts an NVMe device.
func (s *NodeService) formatAndMountNVMeDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	nsid := volumeContext["nsid"]
	nqn := volumeContext["nqn"]
	klog.V(4).Infof("Formatting and mounting NVMe device: device=%s, path=%s, volume=%s, dataset=%s, NQN=%s, NSID=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, nqn, nsid)

	// DEBUG: Log volumeContext to troubleshoot clonedFromSnapshot flag
	keys := []string{}
	for k := range volumeContext {
		keys = append(keys, k)
	}
	klog.V(5).Infof("VolumeContext contains keys: %v", keys)
	if cloned, exists := volumeContext["clonedFromSnapshot"]; exists {
		klog.V(4).Infof("VolumeContext clonedFromSnapshot flag: %s", cloned)
	} else {
		klog.V(5).Infof("VolumeContext does NOT contain clonedFromSnapshot key (new volume, not cloned)")
	}

	// Log device information for troubleshooting
	s.logDeviceInfo(ctx, devicePath)

	// SAFETY CHECK: Verify device size matches expected capacity
	// This helps detect NSID reuse issues where a different ZVOL is presented
	// CRITICAL: If size mismatch is detected, fail the staging to prevent data corruption
	if err := s.verifyDeviceSize(ctx, devicePath, volumeContext); err != nil {
		klog.Errorf("Device size verification FAILED for %s: %v", devicePath, err)
		return nil, status.Errorf(codes.FailedPrecondition,
			"Device size mismatch detected - refusing to mount to prevent data corruption: %v", err)
	}

	// Determine filesystem type from volume capability
	fsType := "ext4" // default
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// CRITICAL: Check if this volume was cloned from a snapshot
	// If clonedFromSnapshot flag is set, add extra stabilization delay before format check
	// ZFS clones inherit the filesystem from the snapshot, but the filesystem metadata
	// takes additional time to propagate through NVMe-oF layers and become visible to the kernel.
	// The device size may be correct, but filesystem signatures (ext4 superblock) need more time.
	isClone := false
	if cloned, exists := volumeContext["clonedFromSnapshot"]; exists && cloned == "true" {
		isClone = true
		klog.V(4).Infof("Volume %s was cloned from snapshot - adding extra stabilization delay before filesystem check", volumeID)
		const cloneStabilizationDelay = 15 * time.Second
		klog.V(4).Infof("Waiting %v for cloned volume %s filesystem metadata to stabilize", cloneStabilizationDelay, devicePath)
		time.Sleep(cloneStabilizationDelay)
		klog.V(4).Infof("Clone stabilization delay complete for %s", devicePath)
	}

	// Check if device needs formatting (will detect existing filesystem or format if needed)
	if err := s.handleDeviceFormatting(ctx, volumeID, devicePath, fsType, datasetName, nqn, nsid, isClone); err != nil {
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
	klog.V(4).Infof("Mounting device %s to %s", devicePath, stagingTargetPath)
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

	klog.V(4).Infof("Mounted NVMe device to staging path")
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
func (s *NodeService) findNVMeDeviceByNQNAndNSID(ctx context.Context, nqn, nsid string) (string, error) {
	klog.V(4).Infof("Searching for NVMe device: NQN=%s, NSID=%s", nqn, nsid)

	// Use nvme list-subsys which shows NQN
	subsysOutput, err := s.runNVMeListSubsys(ctx)
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, falling back to sysfs", err)
		return s.findNVMeDeviceByNQNAndNSIDFromSys(ctx, nqn, nsid)
	}

	// Try to parse the output and find the device
	devicePath := s.parseNVMeListSubsysOutput(subsysOutput, nqn, nsid)
	if devicePath != "" {
		return devicePath, nil
	}

	// Fall back to checking /sys/class/nvme if parsing failed
	return s.findNVMeDeviceByNQNAndNSIDFromSys(ctx, nqn, nsid)
}

// runNVMeListSubsys executes nvme list-subsys and returns the output.
func (s *NodeService) runNVMeListSubsys(ctx context.Context) ([]byte, error) {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	return subsysCmd.CombinedOutput()
}

// parseNVMeListSubsysOutput parses nvme list-subsys JSON output to find device path.
func (s *NodeService) parseNVMeListSubsysOutput(output []byte, nqn, nsid string) string {
	lines := strings.Split(string(output), "\n")
	foundNQN := false

	for i, line := range lines {
		if !strings.Contains(line, nqn) {
			continue
		}

		foundNQN = true
		devicePath := s.extractDevicePathFromLines(lines, i, nsid, nqn)
		if devicePath != "" {
			return devicePath
		}
	}

	if foundNQN {
		klog.Warningf("Found NQN but could not extract device name, falling back to sysfs")
	}
	return ""
}

// extractDevicePathFromLines searches for controller name in lines after the NQN line.
func (s *NodeService) extractDevicePathFromLines(lines []string, startIdx int, nsid, nqn string) string {
	// Look ahead for the "Name" field in the Paths section (up to 20 lines)
	endIdx := startIdx + 20
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	for j := startIdx; j < endIdx; j++ {
		if !strings.Contains(lines[j], "\"Name\"") || !strings.Contains(lines[j], "nvme") {
			continue
		}

		// Extract controller name - format: "Name" : "nvme0"
		parts := strings.Split(lines[j], "\"")
		controllerName := s.extractControllerFromParts(parts)
		if controllerName == "" {
			continue
		}

		// Construct device path using provided nsid
		devicePath := fmt.Sprintf("/dev/%sn%s", controllerName, nsid)
		klog.V(4).Infof("Found NVMe device from list-subsys: %s (controller: %s, NQN: %s, NSID: %s)",
			devicePath, controllerName, nqn, nsid)
		return devicePath
	}
	return ""
}

// extractControllerFromParts extracts controller name from parsed JSON parts.
func (s *NodeService) extractControllerFromParts(parts []string) string {
	for k := range len(parts) - 1 {
		if parts[k] == "Name" && k+2 < len(parts) {
			return strings.TrimSpace(parts[k+2])
		}
	}
	return ""
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
				klog.V(4).Infof("NVMe device found at %s after %d attempts", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("%w after %d attempts", ErrNVMeDeviceTimeout, attempt)
}

// handleDeviceFormatting checks if a device needs formatting and formats it if necessary.
func (s *NodeService) handleDeviceFormatting(ctx context.Context, volumeID, devicePath, fsType, datasetName, nqn, nsid string, isClone bool) error {
	// Check if device is already formatted
	// Use different retry logic: new volumes format quickly, clones wait longer
	needsFormat, err := needsFormatWithRetries(ctx, devicePath, isClone)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to check if device needs formatting: %v", err)
	}

	if needsFormat {
		klog.V(4).Infof("Device %s needs formatting with %s (dataset: %s)", devicePath, fsType, datasetName)
		if formatErr := formatDevice(ctx, volumeID, devicePath, fsType); formatErr != nil {
			return status.Errorf(codes.Internal, "Failed to format device: %v", formatErr)
		}
		return nil
	}

	klog.V(4).Infof("Device %s is already formatted, preserving existing filesystem (dataset: %s, NQN: %s, NSID: %s)",
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

	// Get actual device size using blockdev
	sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sizeCancel()
	sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	if sizeOutput, err := sizeCmd.CombinedOutput(); err == nil {
		deviceSize := strings.TrimSpace(string(sizeOutput))
		klog.V(4).Infof("Device %s has size: %s bytes", devicePath, deviceSize)
	} else {
		klog.Warningf("Failed to get device size for %s: %v", devicePath, err)
	}

	// Try to get device UUID (for better tracking)
	uuidCtx, uuidCancel := context.WithTimeout(ctx, 3*time.Second)
	defer uuidCancel()
	blkidCmd := exec.CommandContext(uuidCtx, "blkid", "-s", "UUID", "-o", "value", devicePath)
	if uuidOutput, err := blkidCmd.CombinedOutput(); err == nil && len(uuidOutput) > 0 {
		uuid := strings.TrimSpace(string(uuidOutput))
		if uuid != "" {
			klog.V(4).Infof("Device %s has filesystem UUID: %s", devicePath, uuid)
		}
	}

	// Try to get filesystem type
	fsTypeCtx, fsTypeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer fsTypeCancel()
	fsCmd := exec.CommandContext(fsTypeCtx, "blkid", "-s", "TYPE", "-o", "value", devicePath)
	if fsOutput, err := fsCmd.CombinedOutput(); err == nil && len(fsOutput) > 0 {
		fsType := strings.TrimSpace(string(fsOutput))
		if fsType != "" {
			klog.V(4).Infof("Device %s has filesystem type: %s", devicePath, fsType)
		}
	}
}

// verifyDeviceSize compares the actual device size with expected capacity from volume context or TrueNAS API.
// This helps detect NSID reuse issues where a stale ZVOL might be presented instead of the expected one.
// Returns an error if the device size does not match the expected capacity (with tolerance for ZFS overhead).
func (s *NodeService) verifyDeviceSize(ctx context.Context, devicePath string, volumeContext map[string]string) error {
	datasetName := volumeContext["datasetName"]

	// Get actual device size
	actualSize, err := getBlockDeviceSize(ctx, devicePath)
	if err != nil {
		return err
	}
	klog.V(4).Infof("Device %s (dataset: %s) actual size: %d bytes (%d GiB)", devicePath, datasetName, actualSize, actualSize/(1024*1024*1024))

	// Get expected capacity from volume context or TrueNAS API
	expectedCapacity := s.getExpectedCapacity(ctx, devicePath, datasetName, volumeContext)

	// If no expected capacity available, skip verification
	if expectedCapacity == 0 {
		klog.Warningf("No expectedCapacity available for device %s (not in volumeContext, API query failed), skipping size verification - this may allow NSID reuse issues to go undetected", devicePath)
		return nil
	}

	// Verify the device size matches expected capacity
	return verifySizeMatch(devicePath, actualSize, expectedCapacity, datasetName, volumeContext)
}

// getBlockDeviceSize returns the size of a block device in bytes.
func getBlockDeviceSize(ctx context.Context, devicePath string) (int64, error) {
	sizeCtx, sizeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sizeCancel()
	sizeCmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	sizeOutput, err := sizeCmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get device size: %w", err)
	}

	actualSize, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse device size: %w", err)
	}
	return actualSize, nil
}

// getExpectedCapacity retrieves the expected capacity from volumeContext or TrueNAS API.
func (s *NodeService) getExpectedCapacity(ctx context.Context, devicePath, datasetName string, volumeContext map[string]string) int64 {
	// Try volume context first
	if expectedCapacityStr := volumeContext["expectedCapacity"]; expectedCapacityStr != "" {
		if capacity, err := strconv.ParseInt(expectedCapacityStr, 10, 64); err == nil {
			return capacity
		}
		klog.Warningf("Failed to parse expectedCapacity '%s' for %s", expectedCapacityStr, devicePath)
	}

	// Query TrueNAS API if not in volumeContext
	if datasetName != "" && s.apiClient != nil {
		klog.V(4).Infof("Querying TrueNAS API for ZVOL size of %s", datasetName)
		dataset, err := s.apiClient.Dataset(ctx, datasetName)
		if err != nil {
			klog.Warningf("Failed to query ZVOL size from TrueNAS API for %s: %v", datasetName, err)
			return 0
		}
		if dataset != nil && dataset.Volsize != nil {
			if parsedSize, ok := dataset.Volsize["parsed"].(float64); ok {
				klog.V(4).Infof("Got expected capacity %d bytes from TrueNAS API for %s", int64(parsedSize), devicePath)
				return int64(parsedSize)
			}
		}
	}
	return 0
}

// verifySizeMatch compares actual and expected sizes with tolerance.
func verifySizeMatch(devicePath string, actualSize, expectedCapacity int64, datasetName string, volumeContext map[string]string) error {
	// Calculate size difference
	sizeDiff := actualSize - expectedCapacity
	if sizeDiff < 0 {
		sizeDiff = -sizeDiff
	}

	// Calculate tolerance: 10% of expected capacity, minimum 100MB
	tolerance := expectedCapacity / 10
	const minTolerance = 100 * 1024 * 1024
	if tolerance < minTolerance {
		tolerance = minTolerance
	}

	if sizeDiff > tolerance {
		klog.Errorf("CRITICAL: Device size mismatch detected for %s!", devicePath)
		klog.Errorf("  Expected capacity: %d bytes (%d GiB)", expectedCapacity, expectedCapacity/(1024*1024*1024))
		klog.Errorf("  Actual device size: %d bytes (%d GiB)", actualSize, actualSize/(1024*1024*1024))
		klog.Errorf("  Difference: %d bytes (%d GiB)", sizeDiff, sizeDiff/(1024*1024*1024))
		klog.Errorf("  This indicates NSID reuse - TrueNAS is presenting a different ZVOL than expected!")
		klog.Errorf("  Dataset: %s, NQN: %s, NSID: %s", datasetName, volumeContext["nqn"], volumeContext["nsid"])
		return fmt.Errorf("%w: expected %d bytes, got %d bytes (diff: %d bytes)",
			ErrDeviceSizeMismatch, expectedCapacity, actualSize, sizeDiff)
	}

	klog.V(4).Infof("Device size verification passed for %s: expected=%d, actual=%d, diff=%d (tolerance=%d)",
		devicePath, expectedCapacity, actualSize, sizeDiff, tolerance)
	return nil
}

// forceDeviceRescan forces the kernel to completely re-read device identity and metadata.
// This is essential when TrueNAS reuses NSIDs after namespace deletion/recreation.
// Without this, the kernel may return cached filesystem metadata from a previous ZVOL.
func forceDeviceRescan(ctx context.Context, devicePath string) error {
	klog.V(4).Infof("Forcing device rescan for %s to clear kernel caches", devicePath)

	// Step 1: Drop page cache related to this device
	// This forces the kernel to discard any cached filesystem metadata
	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Second)
	defer syncCancel()
	syncCmd := exec.CommandContext(syncCtx, "sync")
	if output, err := syncCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("sync command failed: %v, output: %s", err, string(output))
	}

	// Step 2: Flush device buffers
	flushCtx, flushCancel := context.WithTimeout(ctx, 5*time.Second)
	defer flushCancel()
	flushCmd := exec.CommandContext(flushCtx, "blockdev", "--flushbufs", devicePath)
	if output, err := flushCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("blockdev --flushbufs failed for %s: %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Flushed device buffers for %s", devicePath)
	}

	// Step 3: Force kernel to re-read partition table / device geometry
	// This is the key operation that forces the kernel to completely re-probe the device
	rereadCtx, rereadCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rereadCancel()
	rereadCmd := exec.CommandContext(rereadCtx, "blockdev", "--rereadpt", devicePath)
	if output, err := rereadCmd.CombinedOutput(); err != nil {
		// --rereadpt may fail if device is in use or doesn't have partitions
		// This is expected for raw ZVOLs, so we just log it
		klog.V(4).Infof("blockdev --rereadpt for %s (expected for raw ZVOL): %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Re-read partition table for %s", devicePath)
	}

	// Step 4: Trigger udev to re-process the device
	// This ensures udev rules are re-applied and device metadata is refreshed
	udevCtx, udevCancel := context.WithTimeout(ctx, 5*time.Second)
	defer udevCancel()
	udevCmd := exec.CommandContext(udevCtx, "udevadm", "trigger", "--action=change", devicePath)
	if output, err := udevCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger failed for %s: %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev change event for %s", devicePath)
	}

	// Step 5: Wait for udev to settle
	settleCtx, settleCancel := context.WithTimeout(ctx, 10*time.Second)
	defer settleCancel()
	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=10")
	if output, err := settleCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm settle failed: %v, output: %s", err, string(output))
	} else {
		klog.V(4).Infof("udevadm settle completed")
	}

	// Step 6: Read a small amount of data from the device to force kernel I/O
	// This ensures the kernel actually reads from the device, not from cache
	ddCtx, ddCancel := context.WithTimeout(ctx, 5*time.Second)
	defer ddCancel()
	//nolint:gosec // dd with device path from CSI staging context
	ddCmd := exec.CommandContext(ddCtx, "dd", "if="+devicePath, "of=/dev/null", "bs=4096", "count=1", "iflag=direct")
	if output, err := ddCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("dd read failed for %s: %v, output: %s", devicePath, err, string(output))
	} else {
		klog.V(4).Infof("Performed direct read from %s to force kernel I/O", devicePath)
	}

	klog.V(4).Infof("Device rescan completed for %s", devicePath)
	return nil
}

// rescanNVMeNamespace rescans an NVMe namespace to ensure the kernel has fresh device data.
// This is similar to what democratic-csi does with "nvme ns-rescan".
// It forces the kernel to re-read namespace metadata from the target.
func (s *NodeService) rescanNVMeNamespace(ctx context.Context, devicePath string) error {
	// Extract controller path from device path (e.g., /dev/nvme0n1 -> /dev/nvme0)
	// The nvme ns-rescan command operates on the controller, not the namespace device
	controllerPath := extractNVMeController(devicePath)
	if controllerPath == "" {
		return fmt.Errorf("%w: %s", ErrNVMeControllerNotFound, devicePath)
	}

	klog.V(4).Infof("Rescanning NVMe namespace on controller %s (device: %s)", controllerPath, devicePath)

	rescanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// nvme ns-rescan forces the kernel to re-read namespace data from the target
	//nolint:gosec // nvme ns-rescan with controller path derived from device path is expected for CSI driver
	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// ns-rescan may not be supported on all nvme-cli versions
		klog.V(4).Infof("nvme ns-rescan failed for %s: %v, output: %s (this may be OK)", controllerPath, err, string(output))
		return fmt.Errorf("ns-rescan failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully rescanned NVMe namespace on controller %s", controllerPath)
	return nil
}

// extractNVMeController extracts the controller device path from a namespace device path
// (e.g., /dev/nvme0n1 -> /dev/nvme0, /dev/nvme1n2 -> /dev/nvme1).
func extractNVMeController(devicePath string) string {
	// Find the position of 'n' followed by a digit (the namespace part)
	// Device format: /dev/nvmeXnY where X is controller number and Y is namespace number
	for i := len(devicePath) - 1; i >= 0; i-- {
		if devicePath[i] == 'n' && i > 0 && devicePath[i-1] >= '0' && devicePath[i-1] <= '9' {
			// Check if this 'n' is followed by digits (namespace number)
			if i+1 < len(devicePath) && devicePath[i+1] >= '0' && devicePath[i+1] <= '9' {
				return devicePath[:i]
			}
		}
	}
	return ""
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
