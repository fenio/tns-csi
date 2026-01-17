// Package driver implements NVMe-oF-specific CSI controller operations.
package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/retry"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Common error message constants to reduce duplication.
const (
	msgFailedCleanupClonedZVOL = "Failed to cleanup cloned ZVOL: %v"
	// NQN prefix for CSI-managed subsystems.
	// Format: nqn.2137.csi.tns:<volume-name>
	// Each volume gets its own subsystem with NSID=1 (independent subsystem architecture).
	nqnPrefix = "nqn.2137.csi.tns"
)

// Common deletion errors.
var errSubsystemDeletionSkipped = errors.New("subsystem deletion skipped: namespace still exists")

// nvmeofVolumeParams holds validated parameters for NVMe-oF volume creation.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type nvmeofVolumeParams struct {
	requestedCapacity int64
	pool              string
	server            string
	parentDataset     string
	volumeName        string
	zvolName          string
	// Generated NQN for this volume's dedicated subsystem
	subsystemNQN string
	// Optional: port ID to bind the subsystem to (from StorageClass)
	portID int
	// deleteStrategy controls what happens on volume deletion: "delete" (default) or "retain"
	deleteStrategy string
	// markAdoptable marks volumes as adoptable for cross-cluster adoption (StorageClass parameter)
	markAdoptable bool
	// ZFS properties parsed from StorageClass parameters
	zfsProps *zfsZvolProperties
	// Encryption settings parsed from StorageClass and secrets
	encryption *encryptionConfig
	// Adoption metadata from CSI parameters
	pvcName      string
	pvcNamespace string
	storageClass string
}

// zfsZvolProperties holds ZFS properties for ZVOL creation.
// These are parsed from StorageClass parameters with the "zfs." prefix.
type zfsZvolProperties struct {
	Compression  string
	Dedup        string
	Sync         string
	Copies       *int
	Readonly     string
	Sparse       *bool
	Volblocksize string
}

// generateNQN creates a unique NQN for a volume's dedicated subsystem.
// Format: nqn.2137.csi.tns:<volume-name>.
func generateNQN(volumeName string) string {
	return fmt.Sprintf("%s:%s", nqnPrefix, volumeName)
}

// parseZFSZvolProperties extracts ZFS properties for ZVOL creation from StorageClass parameters.
// Parameters with the "zfs." prefix are extracted and the prefix is removed.
// Values are normalized to uppercase as required by TrueNAS API.
// Example: "zfs.compression" -> "compression" = "LZ4".
func parseZFSZvolProperties(params map[string]string) *zfsZvolProperties {
	props := &zfsZvolProperties{}
	hasProps := false

	for key, value := range params {
		if !strings.HasPrefix(key, "zfs.") {
			continue
		}
		propName := strings.TrimPrefix(key, "zfs.")
		hasProps = true

		switch propName {
		case "compression":
			// TrueNAS API requires uppercase: ON, OFF, LZ4, GZIP, ZSTD, etc.
			props.Compression = strings.ToUpper(value)
		case "dedup":
			// TrueNAS API requires uppercase: ON, OFF, VERIFY
			props.Dedup = strings.ToUpper(value)
		case "sync":
			// TrueNAS API requires uppercase: STANDARD, ALWAYS, DISABLED
			props.Sync = strings.ToUpper(value)
		case "copies":
			if copies, err := strconv.Atoi(value); err == nil {
				props.Copies = &copies
			} else {
				klog.Warningf("Invalid zfs.copies value '%s': %v", value, err)
			}
		case "readonly":
			// TrueNAS API requires uppercase: ON, OFF
			props.Readonly = strings.ToUpper(value)
		case "sparse":
			sparse := strings.EqualFold(value, "true") || value == "1"
			props.Sparse = &sparse
		case "volblocksize":
			// Volblocksize can be like "16K" - normalize to uppercase
			props.Volblocksize = strings.ToUpper(value)
		default:
			klog.V(4).Infof("Unknown or unsupported ZFS ZVOL property: %s=%s (ignoring)", propName, value)
		}
	}

	if !hasProps {
		return nil
	}

	klog.V(4).Infof("Parsed ZFS ZVOL properties: compression=%s, dedup=%s, sync=%s, sparse=%v",
		props.Compression, props.Dedup, props.Sync, props.Sparse)
	return props
}

// validateNVMeOFParams validates and extracts NVMe-oF volume parameters from the request.
func validateNVMeOFParams(req *csi.CreateVolumeRequest) (*nvmeofVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NVMe-oF volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Resolve volume name using templating (if configured in StorageClass)
	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}
	zvolName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	// Generate unique NQN for this volume's dedicated subsystem
	subsystemNQN := generateNQN(volumeName)

	// Parse optional port ID from StorageClass parameters
	var portID int
	if portIDStr := params["portID"]; portIDStr != "" {
		var err error
		portID, err = strconv.Atoi(portIDStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid portID parameter: %v", err)
		}
	}

	// Parse ZFS properties from StorageClass parameters
	zfsProps := parseZFSZvolProperties(params)

	// Parse encryption config from StorageClass parameters and secrets
	encryption := parseEncryptionConfig(params, req.GetSecrets())

	// Parse deleteStrategy from StorageClass parameters (default: "delete")
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	// Parse markAdoptable from StorageClass parameters (default: false)
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	// Extract adoption metadata from CSI parameters
	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := params["csi.storage.k8s.io/pvc/namespace"]
	storageClass := params["csi.storage.k8s.io/sc/name"]

	return &nvmeofVolumeParams{
		pool:              pool,
		server:            server,
		parentDataset:     parentDataset,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		zvolName:          zvolName,
		subsystemNQN:      subsystemNQN,
		portID:            portID,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		zfsProps:          zfsProps,
		encryption:        encryption,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
	}, nil
}

// findExistingNVMeOFNamespace finds an existing namespace for a ZVOL in a subsystem.
func (s *ControllerService) findExistingNVMeOFNamespace(ctx context.Context, devicePath string, subsystemID int) (*tnsapi.NVMeOFNamespace, error) {
	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to query NVMe-oF namespaces: %v", err)
	}

	klog.V(4).Infof("Checking for existing namespace: device=%s, subsystem=%d, total namespaces=%d", devicePath, subsystemID, len(namespaces))

	// Log all namespaces for this subsystem to help diagnose NSID conflicts
	subsystemNamespaces := 0
	for _, ns := range namespaces {
		if ns.GetSubsystemID() == subsystemID {
			subsystemNamespaces++
			klog.V(5).Infof("Existing namespace in subsystem %d: ID=%d, NSID=%d, device=%s",
				subsystemID, ns.ID, ns.NSID, ns.GetDevice())
		}
	}
	if subsystemNamespaces > 0 {
		klog.V(4).Infof("Found %d existing namespace(s) in subsystem %d", subsystemNamespaces, subsystemID)
	}

	// Find namespace matching this ZVOL in the target subsystem
	for i := range namespaces {
		ns := &namespaces[i]
		if ns.GetSubsystemID() == subsystemID && ns.GetDevice() == devicePath {
			return ns, nil
		}
	}

	return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil namespace
}

// buildNVMeOFVolumeResponse builds the CreateVolumeResponse for an NVMe-oF volume.
// With independent subsystem architecture, NSID is always 1.
// The nqn parameter should be the NQN returned by TrueNAS (subsystem.NQN), which may differ
// from what we requested. TrueNAS generates its own NQN with a different prefix.
func buildNVMeOFVolumeResponse(volumeName, server, nqn string, zvol *tnsapi.Dataset, subsystem *tnsapi.NVMeOFSubsystem, namespace *tnsapi.NVMeOFNamespace, capacity int64) *csi.CreateVolumeResponse {
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         nqn, // Use the NQN from TrueNAS (subsystem.NQN), not what we requested
	}

	// Volume ID is just the volume name (CSI spec compliant, max 128 bytes)
	volumeID := volumeName

	// Build volume context with all necessary metadata
	volumeContext := buildVolumeContext(meta)
	// NSID is always 1 with independent subsystem architecture
	volumeContext[VolumeContextKeyNSID] = "1"
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(capacity, 10)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNVMeOF, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// handleExistingNVMeOFVolume handles the case when a ZVOL already exists (idempotency).
func (s *ControllerService) handleExistingNVMeOFVolume(ctx context.Context, params *nvmeofVolumeParams, existingZvol *tnsapi.Dataset, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("ZVOL %s already exists (ID: %s), checking idempotency", params.zvolName, existingZvol.ID)

	// Extract existing ZVOL capacity
	existingCapacity := getZvolCapacity(existingZvol)
	if existingCapacity > 0 {
		klog.V(4).Infof("Existing ZVOL capacity: %d bytes, requested: %d bytes", existingCapacity, params.requestedCapacity)

		// Check if capacity matches (CSI idempotency requirement)
		if existingCapacity != params.requestedCapacity {
			timer.ObserveError()
			return nil, false, status.Errorf(codes.AlreadyExists,
				"Volume '%s' already exists with different capacity: existing=%d bytes, requested=%d bytes",
				params.volumeName, existingCapacity, params.requestedCapacity)
		}
	} else {
		// If we can't determine capacity, assume compatible (backward compatibility)
		klog.Warningf("Could not determine capacity for existing ZVOL %s, assuming compatible", params.zvolName)
		existingCapacity = params.requestedCapacity
	}

	// Check if subsystem exists for this volume
	klog.V(4).Infof("Checking for existing subsystem with NQN: %s", params.subsystemNQN)
	subsystem, err := s.apiClient.NVMeOFSubsystemByNQN(ctx, params.subsystemNQN)
	if err != nil {
		// Subsystem doesn't exist - this could mean partial creation, continue to create it
		klog.V(4).Infof("Subsystem not found for existing ZVOL, will create: %v", err)
		return nil, false, nil
	}

	// Check if namespace already exists for this ZVOL
	devicePath := "zvol/" + params.zvolName
	namespace, err := s.findExistingNVMeOFNamespace(ctx, devicePath, subsystem.ID)
	if err != nil {
		timer.ObserveError()
		return nil, false, err
	}

	if namespace != nil {
		// Volume already exists with namespace - return existing volume
		klog.V(4).Infof("NVMe-oF volume already exists (namespace ID: %d, NSID: %d), returning existing volume",
			namespace.ID, namespace.NSID)

		// Use subsystem.NQN (what TrueNAS actually has) not params.subsystemNQN (what we would request)
		resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, subsystem.NQN, existingZvol, subsystem, namespace, existingCapacity)
		timer.ObserveSuccess()
		return resp, true, nil
	}

	// ZVOL exists but no namespace - continue with namespace creation
	return nil, false, nil
}

// getZvolCapacity extracts the capacity from a ZVOL dataset's volsize property.
// Returns the capacity in bytes, or 0 if not found/parseable.
func getZvolCapacity(dataset *tnsapi.Dataset) int64 {
	if dataset == nil || dataset.Volsize == nil {
		klog.V(5).Infof("Dataset has no volsize property")
		return 0
	}

	// TrueNAS returns volsize as a map with "parsed" field containing the integer value
	if parsed, ok := dataset.Volsize["parsed"]; ok {
		switch v := parsed.(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		default:
			klog.Warningf("Unexpected volsize parsed value type: %T", parsed)
		}
	}

	klog.V(5).Infof("Could not extract parsed capacity from volsize: %+v", dataset.Volsize)
	return 0
}

func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "create")
	klog.V(4).Info("Creating NVMe-oF volume (independent subsystem architecture)")

	// Validate and extract parameters
	params, err := validateNVMeOFParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating NVMe-oF volume: %s with size: %d bytes, NQN: %s",
		params.volumeName, params.requestedCapacity, params.subsystemNQN)

	// Check if ZVOL already exists (idempotency)
	existingZvols, err := s.apiClient.QueryAllDatasets(ctx, params.zvolName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing ZVOLs: %v", err)
	}

	// Handle existing ZVOL (idempotency check)
	if len(existingZvols) > 0 {
		resp, done, handleErr := s.handleExistingNVMeOFVolume(ctx, params, &existingZvols[0], timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// If not done, ZVOL exists but no subsystem/namespace - continue with creation
	}

	// Step 1: Create ZVOL
	zvol, err := s.getOrCreateZVOL(ctx, params, existingZvols, timer)
	if err != nil {
		return nil, err
	}

	// Step 2: Create dedicated subsystem for this volume
	subsystem, err := s.createSubsystemForVolume(ctx, params, timer)
	if err != nil {
		// Cleanup: delete ZVOL if subsystem creation fails
		klog.Errorf("Failed to create subsystem, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, err
	}

	// Step 3: Bind subsystem to port (if portID specified or use first available port)
	if bindErr := s.bindSubsystemToPort(ctx, subsystem.ID, params.portID, timer); bindErr != nil {
		// Cleanup: delete subsystem and ZVOL
		klog.Errorf("Failed to bind subsystem to port, cleaning up: %v", bindErr)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, bindErr
	}

	// Step 4: Create NVMe-oF namespace (NSID will be 1 since this is a new subsystem)
	namespace, err := s.createNVMeOFNamespaceForZVOL(ctx, zvol, subsystem, timer)
	if err != nil {
		// Cleanup: delete subsystem and ZVOL
		klog.Errorf("Failed to create namespace, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, err
	}

	// Step 5: Store ZFS user properties for metadata tracking and ownership verification (Schema v1)
	props := tnsapi.NVMeOFVolumePropertiesV1(tnsapi.NVMeOFVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		SubsystemID:    subsystem.ID,
		NamespaceID:    namespace.ID,
		SubsystemNQN:   subsystem.NQN,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
	})
	if err := s.apiClient.SetDatasetProperties(ctx, zvol.ID, props); err != nil {
		// Non-fatal: volume works without properties, but deletion safety is reduced
		klog.Warningf("Failed to set ZFS properties on ZVOL %s: %v (volume will still work)", zvol.ID, err)
	} else {
		klog.V(4).Infof("Set ZFS properties on ZVOL %s: %v", zvol.ID, props)
	}

	// Build and return response
	// Use subsystem.NQN (what TrueNAS actually created) not params.subsystemNQN (what we requested)
	// TrueNAS may assign a different NQN prefix than what we requested
	resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, subsystem.NQN, zvol, subsystem, namespace, params.requestedCapacity)

	klog.Infof("Created NVMe-oF volume: %s (subsystem: %s, NSID: 1)", params.volumeName, subsystem.NQN)
	timer.ObserveSuccess()
	return resp, nil
}

// createSubsystemForVolume creates a dedicated NVMe-oF subsystem for a volume.
func (s *ControllerService) createSubsystemForVolume(ctx context.Context, params *nvmeofVolumeParams, timer *metrics.OperationTimer) (*tnsapi.NVMeOFSubsystem, error) {
	klog.V(4).Infof("Creating dedicated NVMe-oF subsystem: %s", params.subsystemNQN)

	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
		Name:         params.subsystemNQN,
		AllowAnyHost: true, // Allow any initiator to connect
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem: %v", err)
	}

	klog.V(4).Infof("Created NVMe-oF subsystem: ID=%d, Name=%s, NQN=%s", subsystem.ID, subsystem.Name, subsystem.NQN)
	return subsystem, nil
}

// bindSubsystemToPort binds a subsystem to an NVMe-oF port.
// If portID is 0, it uses the first available port.
func (s *ControllerService) bindSubsystemToPort(ctx context.Context, subsystemID, portID int, timer *metrics.OperationTimer) error {
	// If no specific port requested, find the first available port
	if portID == 0 {
		ports, err := s.apiClient.QueryNVMeOFPorts(ctx)
		if err != nil {
			timer.ObserveError()
			return status.Errorf(codes.Internal, "Failed to query NVMe-oF ports: %v", err)
		}
		if len(ports) == 0 {
			timer.ObserveError()
			return status.Error(codes.FailedPrecondition,
				"No NVMe-oF ports configured. Create a port in TrueNAS (Shares > NVMe-oF Targets > Ports) first.")
		}
		portID = ports[0].ID
		klog.Infof("Using first available NVMe-oF port: ID=%d", portID)
	}

	klog.Infof("Binding subsystem %d to port %d", subsystemID, portID)
	if err := s.apiClient.AddSubsystemToPort(ctx, subsystemID, portID); err != nil {
		timer.ObserveError()
		return status.Errorf(codes.Internal, "Failed to bind subsystem to port: %v", err)
	}

	klog.Infof("Successfully bound subsystem %d to port %d", subsystemID, portID)
	return nil
}

// getOrCreateZVOL gets an existing ZVOL or creates a new one.
func (s *ControllerService) getOrCreateZVOL(ctx context.Context, params *nvmeofVolumeParams, existingZvols []tnsapi.Dataset, timer *metrics.OperationTimer) (*tnsapi.Dataset, error) {
	if len(existingZvols) > 0 {
		zvol := &existingZvols[0]
		klog.V(4).Infof("Using existing ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
		return zvol, nil
	}

	// Build ZVOL creation parameters with ZFS properties
	createParams := tnsapi.ZvolCreateParams{
		Name:         params.zvolName,
		Type:         "VOLUME",
		Volsize:      params.requestedCapacity,
		Volblocksize: "16K", // Default block size for NVMe-oF
	}

	// Apply ZFS properties if specified in StorageClass
	if params.zfsProps != nil {
		createParams.Compression = params.zfsProps.Compression
		createParams.Dedup = params.zfsProps.Dedup
		createParams.Sync = params.zfsProps.Sync
		createParams.Copies = params.zfsProps.Copies
		createParams.Readonly = params.zfsProps.Readonly
		createParams.Sparse = params.zfsProps.Sparse

		// Override default volblocksize if specified
		if params.zfsProps.Volblocksize != "" {
			createParams.Volblocksize = params.zfsProps.Volblocksize
		}

		klog.V(4).Infof("Creating ZVOL with ZFS properties: compression=%s, dedup=%s, sync=%s, sparse=%v, volblocksize=%s",
			createParams.Compression, createParams.Dedup, createParams.Sync, createParams.Sparse, createParams.Volblocksize)
	}

	// Apply encryption settings if specified in StorageClass
	if params.encryption != nil && params.encryption.Enabled { //nolint:dupl // Intentionally duplicated in NFS
		createParams.Encryption = true
		// Must disable inherit_encryption when enabling encryption
		inheritEncryption := false
		createParams.InheritEncryption = &inheritEncryption

		// Build encryption options
		encOpts := &tnsapi.EncryptionOptions{
			Algorithm: params.encryption.Algorithm,
		}

		// Determine key source (priority: passphrase > key > generateKey)
		switch {
		case params.encryption.Passphrase != "":
			encOpts.Passphrase = params.encryption.Passphrase
		case params.encryption.Key != "":
			encOpts.Key = params.encryption.Key
		case params.encryption.GenerateKey:
			encOpts.GenerateKey = true
		}

		createParams.EncryptionOptions = encOpts

		klog.V(4).Infof("Creating encrypted ZVOL with algorithm=%s, generateKey=%v, hasPassphrase=%v, hasKey=%v",
			params.encryption.Algorithm, params.encryption.GenerateKey,
			params.encryption.Passphrase != "", params.encryption.Key != "")
	}

	// Create new ZVOL
	zvol, err := s.apiClient.CreateZvol(ctx, createParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", err)
	}

	klog.V(4).Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
	return zvol, nil
}

// createNVMeOFNamespaceForZVOL creates an NVMe-oF namespace for a ZVOL.
// With independent subsystem architecture, NSID is always 1.
func (s *ControllerService) createNVMeOFNamespaceForZVOL(ctx context.Context, zvol *tnsapi.Dataset, subsystem *tnsapi.NVMeOFSubsystem, timer *metrics.OperationTimer) (*tnsapi.NVMeOFNamespace, error) {
	devicePath := "zvol/" + zvol.Name

	klog.V(4).Infof("Creating NVMe-oF namespace for device: %s in subsystem %d (ZVOL ID: %s)", devicePath, subsystem.ID, zvol.ID)

	// With independent subsystem architecture, NSID is always 1 (first namespace in new subsystem)
	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		NSID:       1, // Always NSID 1 with independent subsystems
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.V(4).Infof("Created NVMe-oF namespace: ID=%d, NSID=%d, device=%s, subsystem=%d",
		namespace.ID, namespace.NSID, devicePath, subsystem.ID)
	return namespace, nil
}

// verifyNVMeOFOwnership verifies ownership of an NVMe-oF volume via ZFS properties.
// It returns an error if ownership verification fails, or nil if verification passes
// or properties are not available (for backward compatibility with older volumes).
// If verification passes, it may update meta with stored IDs from ZFS properties.
// Also returns the deleteStrategy from ZFS properties (defaults to "delete" if not found).
func (s *ControllerService) verifyNVMeOFOwnership(ctx context.Context, meta *VolumeMetadata) (string, error) {
	deleteStrategy := tnsapi.DeleteStrategyDelete // Default to delete

	if meta.DatasetID == "" {
		return deleteStrategy, nil
	}

	props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, []string{
		tnsapi.PropertyManagedBy,
		tnsapi.PropertyCSIVolumeName,
		tnsapi.PropertyNVMeSubsystemID,
		tnsapi.PropertyNVMeNamespaceID,
		tnsapi.PropertyNVMeSubsystemNQN,
		tnsapi.PropertyDeleteStrategy,
	})
	if err != nil {
		// Properties not found - could be old volume or manual creation
		klog.V(4).Infof("Could not read ZFS properties for %s: %v (proceeding with metadata-based deletion)", meta.DatasetID, err)
		return deleteStrategy, nil
	}

	// Verify managed_by property
	if managedBy, ok := props[tnsapi.PropertyManagedBy]; ok && managedBy != tnsapi.ManagedByValue {
		return "", status.Errorf(codes.FailedPrecondition,
			"ZVOL %s is not managed by tns-csi (managed_by=%s), refusing to delete",
			meta.DatasetID, managedBy)
	}

	// Verify volume name matches
	if storedVolumeName, ok := props[tnsapi.PropertyCSIVolumeName]; ok && storedVolumeName != meta.Name {
		return "", status.Errorf(codes.FailedPrecondition,
			"Volume name mismatch: ZVOL %s belongs to volume '%s', not '%s' (possible ID reuse)",
			meta.DatasetID, storedVolumeName, meta.Name)
	}

	// Use stored IDs if available (more reliable than metadata after TrueNAS restart)
	if storedSubsystemID, ok := props[tnsapi.PropertyNVMeSubsystemID]; ok {
		if parsedID := tnsapi.StringToInt(storedSubsystemID); parsedID > 0 && parsedID != meta.NVMeOFSubsystemID {
			klog.Infof("Using stored subsystem ID %d instead of metadata ID %d", parsedID, meta.NVMeOFSubsystemID)
			meta.NVMeOFSubsystemID = parsedID
		}
	}
	if storedNamespaceID, ok := props[tnsapi.PropertyNVMeNamespaceID]; ok {
		if parsedID := tnsapi.StringToInt(storedNamespaceID); parsedID > 0 && parsedID != meta.NVMeOFNamespaceID {
			klog.Infof("Using stored namespace ID %d instead of metadata ID %d", parsedID, meta.NVMeOFNamespaceID)
			meta.NVMeOFNamespaceID = parsedID
		}
	}

	// Get deleteStrategy from properties
	if strategy, ok := props[tnsapi.PropertyDeleteStrategy]; ok && strategy != "" {
		deleteStrategy = strategy
	}

	klog.V(4).Infof("Ownership verified for ZVOL %s (volume: %s)", meta.DatasetID, meta.Name)
	return deleteStrategy, nil
}

// deleteNVMeOFVolume deletes an NVMe-oF volume.
// With independent subsystem architecture, this deletes the namespace, subsystem, and ZVOL.
// Uses best-effort cleanup: continues deleting resources even if earlier steps fail.
// This prevents orphaned resources on TrueNAS when partial failures occur.
// If deleteStrategy is "retain", the volume is kept but CSI returns success.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "delete")
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, namespace ID: %d, subsystem ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFNamespaceID, meta.NVMeOFSubsystemID)

	// Step 0: Verify ownership via ZFS properties before deletion
	// This prevents accidental deletion of resources when TrueNAS reuses IDs
	// Also retrieves the deleteStrategy
	deleteStrategy, err := s.verifyNVMeOFOwnership(ctx, meta)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	// Check if we should retain the volume instead of deleting
	if deleteStrategy == tnsapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has deleteStrategy=retain, skipping actual deletion (ZVOL: %s, namespace ID: %d, subsystem ID: %d will be kept)",
			meta.Name, meta.DatasetID, meta.NVMeOFNamespaceID, meta.NVMeOFSubsystemID)
		// Return success per CSI spec - the PV is "deleted" from Kubernetes perspective
		// but the underlying storage is retained
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Track all errors but continue with best-effort cleanup
	var deletionErrors []error

	// Step 1: Delete NVMe-oF namespace (best effort)
	namespaceDeleted := false
	if err := s.deleteNVMeOFNamespace(ctx, meta); err != nil {
		klog.Errorf("Failed to delete namespace %d (continuing with cleanup): %v", meta.NVMeOFNamespaceID, err)
		deletionErrors = append(deletionErrors, fmt.Errorf("namespace deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted namespace %d", meta.NVMeOFNamespaceID)
		namespaceDeleted = true
	}

	// Step 2: Delete NVMe-oF subsystem (best effort - independent subsystem architecture)
	// Only attempt if namespace was successfully deleted (TrueNAS won't delete subsystems with active namespaces)
	// If namespace deletion failed, skip subsystem deletion (it will fail anyway)
	if !namespaceDeleted && meta.NVMeOFNamespaceID > 0 {
		klog.Warningf("Skipping subsystem deletion because namespace %d deletion failed - subsystem %d cannot be deleted while namespace exists",
			meta.NVMeOFNamespaceID, meta.NVMeOFSubsystemID)
		deletionErrors = append(deletionErrors, errSubsystemDeletionSkipped)
	} else if err := s.deleteNVMeOFSubsystem(ctx, meta); err != nil {
		klog.Errorf("Failed to delete subsystem %d (continuing with cleanup): %v", meta.NVMeOFSubsystemID, err)
		deletionErrors = append(deletionErrors, fmt.Errorf("subsystem deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted subsystem %d", meta.NVMeOFSubsystemID)
	}

	// Step 3: Delete ZVOL (best effort)
	if err := s.deleteZVOL(ctx, meta); err != nil {
		klog.Errorf("Failed to delete ZVOL %s (continuing with cleanup): %v", meta.DatasetID, err)
		deletionErrors = append(deletionErrors, fmt.Errorf("ZVOL deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted ZVOL %s", meta.DatasetID)
	}

	// Evaluate cleanup results
	if len(deletionErrors) == 0 {
		// Complete success - all resources deleted
		klog.Infof("Deleted NVMe-oF volume: %s (namespace, subsystem, and ZVOL)", meta.Name)
		metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Partial or complete failure - return error to trigger retry
	// This prevents orphaned resources on TrueNAS by ensuring Kubernetes retries until all resources are cleaned
	klog.Errorf("Failed to delete %d of 3 resources for volume %s: %v", len(deletionErrors), meta.Name, deletionErrors)
	klog.Infof("Successfully deleted %d of 3 resources (namespace, subsystem, ZVOL) - will retry remaining", 3-len(deletionErrors))

	// Provide helpful context based on which resources failed
	errorDetailParts := make([]string, 0, len(deletionErrors)+1)
	errorDetailParts = append(errorDetailParts, "Failed to delete volume resources:")
	for i, err := range deletionErrors {
		errorDetailParts = append(errorDetailParts, fmt.Sprintf("  %d. %v", i+1, err))
	}
	var builder strings.Builder
	for _, part := range errorDetailParts {
		builder.WriteString("\n")
		builder.WriteString(part)
	}
	errorDetails := builder.String()

	timer.ObserveError()
	return nil, status.Errorf(codes.Internal,
		"Failed to delete %d of 3 volume resources for %s (successfully deleted %d): %s",
		len(deletionErrors), meta.Name, 3-len(deletionErrors), errorDetails)
}

// deleteNVMeOFSubsystem deletes an NVMe-oF subsystem with retry logic for busy resources.
// This function first verifies all namespaces are removed, then unbinds from ports, then deletes the subsystem.
// TrueNAS will refuse to delete subsystems with active namespaces or port bindings.
func (s *ControllerService) deleteNVMeOFSubsystem(ctx context.Context, meta *VolumeMetadata) error {
	if meta.NVMeOFSubsystemID <= 0 {
		return nil
	}

	klog.V(4).Infof("Deleting NVMe-oF subsystem: ID=%d (with retry for busy resources)", meta.NVMeOFSubsystemID)

	// Step 1: Verify no namespaces are attached to this subsystem
	// TrueNAS will refuse to delete subsystems with active namespaces
	namespaces, err := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if err != nil {
		klog.Warningf("Failed to query namespaces for subsystem cleanup verification (continuing anyway): %v", err)
	} else {
		// Count namespaces still attached to this subsystem
		attachedCount := 0
		for _, ns := range namespaces {
			if ns.GetSubsystemID() == meta.NVMeOFSubsystemID {
				attachedCount++
				klog.Warningf("Namespace %d (NSID: %d, device: %s) still attached to subsystem %d",
					ns.ID, ns.NSID, ns.GetDevice(), meta.NVMeOFSubsystemID)
			}
		}
		if attachedCount > 0 {
			e := status.Errorf(codes.FailedPrecondition,
				"Cannot delete subsystem %d: %d namespace(s) still attached. TrueNAS requires all namespaces to be deleted first.",
				meta.NVMeOFSubsystemID, attachedCount)
			klog.Error(e)
			// Don't call timer.ObserveError() here - let the caller handle it
			return e
		}
		klog.V(4).Infof("Verified no namespaces attached to subsystem %d", meta.NVMeOFSubsystemID)
	}

	// Step 2: Query and unbind all port associations
	// TrueNAS may silently fail to delete subsystems with active port bindings
	bindings, err := s.apiClient.QuerySubsystemPortBindings(ctx, meta.NVMeOFSubsystemID)
	if err != nil {
		klog.Warningf("Failed to query port bindings for subsystem %d (continuing anyway): %v",
			meta.NVMeOFSubsystemID, err)
	} else if len(bindings) > 0 {
		klog.V(4).Infof("Unbinding subsystem %d from %d port(s)", meta.NVMeOFSubsystemID, len(bindings))
		for _, binding := range bindings {
			if unbindErr := s.apiClient.RemoveSubsystemFromPort(ctx, binding.ID); unbindErr != nil {
				// Log warning but continue - we still want to try deleting the subsystem
				klog.Warningf("Failed to unbind subsystem %d from port binding %d (continuing anyway): %v",
					meta.NVMeOFSubsystemID, binding.ID, unbindErr)
			} else {
				klog.V(4).Infof("Unbound subsystem %d from port binding %d", meta.NVMeOFSubsystemID, binding.ID)
			}
		}
	}

	// Step 3: Delete the subsystem with retry logic for busy resources
	retryConfig := retry.DeletionConfig("delete-nvmeof-subsystem")
	err = retry.WithRetryNoResult(ctx, retryConfig, func() error {
		deleteErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, meta.NVMeOFSubsystemID)
		if deleteErr != nil && isNotFoundError(deleteErr) {
			// Subsystem already deleted - not an error (idempotency)
			klog.V(4).Infof("Subsystem %d not found, assuming already deleted (idempotency)", meta.NVMeOFSubsystemID)
			return nil
		}
		return deleteErr
	})

	if err != nil {
		// All retries exhausted or non-retryable error
		e := status.Errorf(codes.Internal, "Failed to delete NVMe-oF subsystem %d: %v",
			meta.NVMeOFSubsystemID, err)
		klog.Error(e)
		return e
	}

	klog.V(4).Infof("Deleted NVMe-oF subsystem %d", meta.NVMeOFSubsystemID)
	return nil
}

// deleteNVMeOFNamespace deletes an NVMe-oF namespace with retry logic for busy resources.
func (s *ControllerService) deleteNVMeOFNamespace(ctx context.Context, meta *VolumeMetadata) error {
	if meta.NVMeOFNamespaceID <= 0 {
		return nil
	}

	klog.V(4).Infof("Deleting NVMe-oF namespace: ID=%d, ZVOL=%s, dataset=%s (with retry for busy resources)",
		meta.NVMeOFNamespaceID, meta.DatasetID, meta.DatasetName)

	retryConfig := retry.DeletionConfig("delete-nvmeof-namespace")
	err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
		deleteErr := s.apiClient.DeleteNVMeOFNamespace(ctx, meta.NVMeOFNamespaceID)
		if deleteErr != nil && isNotFoundError(deleteErr) {
			// Namespace already deleted - not an error (idempotency)
			klog.V(4).Infof("Namespace %d not found, assuming already deleted (idempotency)", meta.NVMeOFNamespaceID)
			return nil
		}
		return deleteErr
	})

	if err != nil {
		// All retries exhausted or non-retryable error
		e := status.Errorf(codes.Internal, "Failed to delete NVMe-oF namespace %d (ZVOL: %s): %v",
			meta.NVMeOFNamespaceID, meta.DatasetID, err)
		klog.Error(e)
		return e
	}

	klog.V(4).Infof("Deleted NVMe-oF namespace %d (ZVOL: %s)", meta.NVMeOFNamespaceID, meta.DatasetID)

	// Verify namespace is gone
	return s.verifyNamespaceDeletion(ctx, meta)
}

// verifyNamespaceDeletion verifies that a namespace has been fully deleted.
func (s *ControllerService) verifyNamespaceDeletion(ctx context.Context, meta *VolumeMetadata) error {
	klog.V(4).Infof("Verifying namespace %d deletion...", meta.NVMeOFNamespaceID)

	allNamespaces, queryErr := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
	if queryErr != nil {
		// Query error - log but don't fail the deletion
		klog.V(4).Infof("Could not verify namespace deletion: %v", queryErr)
		return nil
	}

	// Check if namespace still exists
	for _, ns := range allNamespaces {
		if ns.ID != meta.NVMeOFNamespaceID {
			continue
		}
		// Namespace still exists - return error to retry
		e := status.Errorf(codes.Internal, "Namespace %d still exists after deletion (NSID: %d, device: %s)",
			ns.ID, ns.NSID, ns.GetDevice())
		klog.Error(e)
		// Don't call timer.ObserveError() here - let the caller handle it
		return e
	}

	klog.V(4).Infof("Verified namespace %d is fully deleted", meta.NVMeOFNamespaceID)
	return nil
}

// deleteZVOL deletes a ZVOL dataset with retry logic for busy resources.
func (s *ControllerService) deleteZVOL(ctx context.Context, meta *VolumeMetadata) error {
	if meta.DatasetID == "" {
		klog.Infof("deleteZVOL: DatasetID is empty, skipping deletion")
		return nil
	}

	klog.Infof("deleteZVOL: Starting deletion of ZVOL %s for volume %s", meta.DatasetID, meta.Name)

	retryConfig := retry.DeletionConfig("delete-zvol")
	err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
		deleteErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
		if deleteErr != nil && isNotFoundError(deleteErr) {
			// ZVOL already deleted - not an error (idempotency)
			klog.V(4).Infof("ZVOL %s not found, assuming already deleted (idempotency)", meta.DatasetID)
			return nil
		}
		return deleteErr
	})

	if err != nil {
		// All retries exhausted or non-retryable error
		klog.Errorf("deleteZVOL: Failed to delete ZVOL %s: %v", meta.DatasetID, err)
		return status.Errorf(codes.Internal, "Failed to delete ZVOL %s: %v", meta.DatasetID, err)
	}

	klog.Infof("deleteZVOL: Successfully deleted ZVOL %s for volume %s", meta.DatasetID, meta.Name)
	return nil
}

// setupNVMeOFVolumeFromClone sets up NVMe-oF infrastructure for a cloned ZVOL.
// With independent subsystem architecture, creates a new subsystem for the clone.
func (s *ControllerService) setupNVMeOFVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server, _, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("Setting up NVMe-oF namespace for cloned ZVOL: %s (from snapshot, type: %s)", zvol.Name, zvol.Type)

	volumeName := req.GetName()
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "clone")

	// Validate that the dataset is a ZVOL (type=VOLUME), not a filesystem
	// This can happen if detached snapshot was created incorrectly
	if zvol.Type != "VOLUME" {
		klog.Errorf("Expected ZVOL (type=VOLUME) but got type=%q for dataset %s. "+
			"This can happen if the source detached snapshot was not a ZVOL.", zvol.Type, zvol.Name)
		// Cleanup the non-ZVOL dataset
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup non-ZVOL dataset: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Cannot create NVMe-oF volume from snapshot: cloned dataset %s has type %q, expected VOLUME (ZVOL). "+
				"The source detached snapshot may not have been created correctly from an NVMe-oF volume.",
			zvol.Name, zvol.Type)
	}

	// Generate NQN for the cloned volume's dedicated subsystem
	subsystemNQN := generateNQN(volumeName)
	klog.Infof("Generated NQN for cloned volume: %s", subsystemNQN)

	// Parse optional port ID from StorageClass parameters
	params := req.GetParameters()
	var portID int
	if portIDStr := params["portID"]; portIDStr != "" {
		var err error
		portID, err = strconv.Atoi(portIDStr)
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.InvalidArgument, "invalid portID parameter: %v", err)
		}
	}

	// Step 1: Create dedicated subsystem for the cloned volume
	klog.Infof("Creating dedicated NVMe-oF subsystem for clone: %s", subsystemNQN)
	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
		Name:         subsystemNQN,
		AllowAnyHost: true,
	})
	if err != nil {
		// Cleanup: delete the cloned ZVOL if subsystem creation fails
		klog.Errorf("Failed to create NVMe-oF subsystem, cleaning up cloned ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf(msgFailedCleanupClonedZVOL, delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem: %v", err)
	}

	klog.Infof("Created NVMe-oF subsystem: ID=%d, Name=%s", subsystem.ID, subsystem.Name)

	// Step 2: Bind subsystem to port
	if bindErr := s.bindSubsystemToPort(ctx, subsystem.ID, portID, timer); bindErr != nil {
		// Cleanup: delete subsystem and cloned ZVOL
		klog.Errorf("Failed to bind subsystem to port, cleaning up: %v", bindErr)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf(msgFailedCleanupClonedZVOL, delErr)
		}
		return nil, bindErr
	}

	// Step 3: Create NVMe-oF namespace (NSID = 1)
	devicePath := "zvol/" + zvol.Name
	klog.Infof("Creating NVMe-oF namespace for device: %s in subsystem %d", devicePath, subsystem.ID)

	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		NSID:       1, // Always NSID 1 with independent subsystems
	})
	if err != nil {
		// Cleanup: delete subsystem and cloned ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf(msgFailedCleanupClonedZVOL, delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.Infof("Created NVMe-oF namespace: ID=%d, NSID=%d", namespace.ID, namespace.NSID)

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Get deleteStrategy from StorageClass parameters (default: "delete")
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	// Step 4: Store ZFS user properties for metadata tracking and ownership verification (Schema v1)
	props := tnsapi.NVMeOFVolumePropertiesV1(tnsapi.NVMeOFVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		SubsystemID:    subsystem.ID,
		NamespaceID:    namespace.ID,
		SubsystemNQN:   subsystem.NQN,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
	})
	// Add clone source properties
	for k, v := range tnsapi.ClonedVolumeProperties(tnsapi.ContentSourceSnapshot, snapshotID) {
		props[k] = v
	}
	if err := s.apiClient.SetDatasetProperties(ctx, zvol.ID, props); err != nil {
		// Non-fatal: volume works without properties, but deletion safety is reduced
		klog.Warningf("Failed to set ZFS properties on cloned ZVOL %s: %v (volume will still work)", zvol.ID, err)
	} else {
		klog.V(4).Infof("Set ZFS properties on cloned ZVOL %s: %v", zvol.ID, props)
	}

	// Build volume metadata
	// IMPORTANT: Use subsystem.NQN (the full NQN from TrueNAS including UUID prefix),
	// not subsystemNQN (the short name we generated). TrueNAS adds a UUID prefix to create
	// the subnqn, and the node plugin must use this full NQN to connect.
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN, // Use full NQN from TrueNAS (subnqn), not short name
	}

	// Volume ID is just the volume name (CSI spec compliant)
	volumeID := volumeName

	// Construct volume context with metadata for node plugin
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyNSID] = "1" // Always NSID 1 with independent subsystems
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(requestedCapacity, 10)
	// CRITICAL: Mark this volume as cloned from snapshot in VolumeContext
	// This signals to the node that the volume has existing data and should NEVER be formatted
	volumeContext[VolumeContextKeyClonedFromSnap] = VolumeContextValueTrue

	klog.Infof("Created NVMe-oF volume from snapshot: %s (subsystem: %s, NSID: 1)", volumeName, subsystem.NQN)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNVMeOF, requestedCapacity)

	timer.ObserveSuccess()
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
			ContentSource: &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Snapshot{
					Snapshot: &csi.VolumeContentSource_SnapshotSource{
						SnapshotId: snapshotID,
					},
				},
			},
		},
	}, nil
}

// adoptNVMeOFVolume adopts an orphaned NVMe-oF volume by re-creating its subsystem and namespace.
// This is called when a volume is found by CSI name but needs to be adopted into a new cluster.
func (s *ControllerService) adoptNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.DatasetWithProperties, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting NVMe-oF volume: %s (dataset=%s)", volumeName, dataset.ID)

	// Get server parameter
	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Parse optional port ID from StorageClass parameters
	var portID int
	if portIDStr := params["portID"]; portIDStr != "" {
		var err error
		portID, err = strconv.Atoi(portIDStr)
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.InvalidArgument, "invalid portID parameter: %v", err)
		}
	}

	// Check if subsystem already exists (by looking up stored NQN in properties)
	var subsystem *tnsapi.NVMeOFSubsystem
	var namespace *tnsapi.NVMeOFNamespace
	devicePath := "zvol/" + dataset.Name

	// Try to find existing subsystem by stored NQN
	if nqnProp, ok := dataset.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok && nqnProp.Value != "" {
		existingSubsys, err := s.apiClient.NVMeOFSubsystemByNQN(ctx, nqnProp.Value)
		if err == nil && existingSubsys != nil {
			subsystem = existingSubsys
			klog.Infof("Found existing subsystem for adopted volume: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

			// Check if namespace exists in this subsystem
			ns, err := s.findExistingNVMeOFNamespace(ctx, devicePath, subsystem.ID)
			if err == nil && ns != nil {
				namespace = ns
				klog.Infof("Found existing namespace for adopted volume: ID=%d, NSID=%d", namespace.ID, namespace.NSID)
			}
		}
	}

	// If no subsystem found, create new one
	if subsystem == nil {
		subsystemNQN := generateNQN(volumeName)
		klog.Infof("Creating new subsystem for adopted volume: %s", subsystemNQN)

		newSubsys, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
			Name:         subsystemNQN,
			AllowAnyHost: true,
		})
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create subsystem for adopted volume: %v", err)
		}
		subsystem = newSubsys
		klog.Infof("Created subsystem for adopted volume: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

		// Bind to port
		if bindErr := s.bindSubsystemToPort(ctx, subsystem.ID, portID, timer); bindErr != nil {
			// Cleanup subsystem on failure
			if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
				klog.Errorf("Failed to cleanup subsystem after port bind failure: %v", delErr)
			}
			return nil, bindErr
		}
	}

	// If no namespace found, create one
	if namespace == nil {
		klog.Infof("Creating namespace for adopted volume: device=%s, subsystem=%d", devicePath, subsystem.ID)

		newNS, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
			SubsysID:   subsystem.ID,
			DevicePath: devicePath,
			DeviceType: "ZVOL",
			NSID:       1, // Always NSID 1 with independent subsystems
		})
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create namespace for adopted volume: %v", err)
		}
		namespace = newNS
		klog.Infof("Created namespace for adopted volume: ID=%d, NSID=%d", namespace.ID, namespace.NSID)
	}

	// Update ZFS properties with new IDs
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := tnsapi.NVMeOFVolumePropertiesV1(tnsapi.NVMeOFVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		SubsystemID:    subsystem.ID,
		NamespaceID:    namespace.ID,
		SubsystemNQN:   subsystem.NQN,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
	})
	if propErr := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); propErr != nil {
		klog.Warningf("Failed to update ZFS properties on adopted volume %s: %v", dataset.ID, propErr)
	}

	// Build response
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         dataset.ID,
		DatasetName:       dataset.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyNSID] = "1"
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(requestedCapacity, 10)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolNVMeOF, requestedCapacity)

	klog.Infof("Successfully adopted NVMe-oF volume: %s (subsystem=%s, namespaceID=%d)", volumeName, subsystem.NQN, namespace.ID)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeName,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// expandNVMeOFVolume expands an NVMe-oF volume by updating the ZVOL size.
//
//nolint:dupl // Similar to expandNFSVolume but with different parameters (Volsize vs Quota, NodeExpansionRequired)
func (s *ControllerService) expandNVMeOFVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "expand")
	klog.V(4).Infof("Expanding NVMe-oF volume: %s (ZVOL: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	// For NVMe-oF volumes (ZVOLs), we update the volsize property
	klog.V(4).Infof("Expanding NVMe-oF ZVOL - DatasetID: %s, DatasetName: %s, New Size: %d bytes",
		meta.DatasetID, meta.DatasetName, requiredBytes)

	updateParams := tnsapi.DatasetUpdateParams{
		Volsize: &requiredBytes,
	}

	_, err := s.apiClient.UpdateDataset(ctx, meta.DatasetID, updateParams)
	if err != nil {
		// Provide detailed error information to help diagnose dataset issues
		klog.Errorf("Failed to update ZVOL %s (Name: %s): %v", meta.DatasetID, meta.DatasetName, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update ZVOL size for dataset '%s' (Name: '%s'). "+
				"The dataset may not exist on TrueNAS - verify it exists at Storage > Pools. "+
				"Error: %v", meta.DatasetID, meta.DatasetName, err)
	}

	klog.Infof("Expanded NVMe-oF volume: %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric using plain volume name
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: true, // NVMe-oF volumes require node-side filesystem expansion
	}, nil
}
