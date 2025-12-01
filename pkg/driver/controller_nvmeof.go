// Package driver implements NVMe-oF-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Common error message constants to reduce duplication.
const (
	msgVerifyingNVMeOFSubsystem = "Verifying NVMe-oF subsystem exists with NQN: %s"
	msgSubsystemNotFound        = "Failed to find NVMe-oF subsystem with NQN '%s'. " +
		"Pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems) " +
		"with ports attached before provisioning volumes. Error: %v"
)

// nvmeofVolumeParams holds validated parameters for NVMe-oF volume creation.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type nvmeofVolumeParams struct {
	requestedCapacity int64
	pool              string
	server            string
	subsystemNQN      string
	parentDataset     string
	volumeName        string
	zvolName          string
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

	subsystemNQN := params["subsystemNQN"]
	if subsystemNQN == "" {
		return nil, status.Error(codes.InvalidArgument,
			"subsystemNQN parameter is required for NVMe-oF volumes. "+
				"Pre-configure an NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
				"and provide its NQN in the StorageClass parameters.")
	}

	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	volumeName := req.GetName()
	zvolName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	return &nvmeofVolumeParams{
		pool:              pool,
		server:            server,
		subsystemNQN:      subsystemNQN,
		parentDataset:     parentDataset,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		zvolName:          zvolName,
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
		if ns.Subsystem == subsystemID {
			subsystemNamespaces++
			klog.V(5).Infof("Existing namespace in subsystem %d: ID=%d, NSID=%d, device=%s",
				subsystemID, ns.ID, ns.NSID, ns.Device)
		}
	}
	if subsystemNamespaces > 0 {
		klog.V(4).Infof("Found %d existing namespace(s) in subsystem %d", subsystemNamespaces, subsystemID)
	}

	// Find namespace matching this ZVOL in the target subsystem
	for i := range namespaces {
		ns := &namespaces[i]
		if ns.Subsystem == subsystemID && ns.Device == devicePath {
			return ns, nil
		}
	}

	return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil namespace
}

// buildNVMeOFVolumeResponse builds the CreateVolumeResponse for an NVMe-oF volume.
func buildNVMeOFVolumeResponse(volumeName, server string, zvol *tnsapi.Dataset, subsystem *tnsapi.NVMeOFSubsystem, namespace *tnsapi.NVMeOFNamespace, capacity int64) (*csi.CreateVolumeResponse, error) {
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
		SubsystemNQN:      subsystem.NQN,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID: %v", err)
	}

	volumeContext := map[string]string{
		"server":            server,
		"nqn":               subsystem.NQN,
		"datasetID":         zvol.ID,
		"datasetName":       zvol.Name,
		"nvmeofSubsystemID": strconv.Itoa(subsystem.ID),
		"nvmeofNamespaceID": strconv.Itoa(namespace.ID),
		"nsid":              strconv.Itoa(namespace.NSID),
		"expectedCapacity":  strconv.FormatInt(capacity, 10),
	}

	// Record volume capacity metric
	metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}, nil
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

	// Verify subsystem exists
	klog.V(4).Infof(msgVerifyingNVMeOFSubsystem, params.subsystemNQN)
	subsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, params.subsystemNQN)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.FailedPrecondition, msgSubsystemNotFound, params.subsystemNQN, err)
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

		resp, err := buildNVMeOFVolumeResponse(params.volumeName, params.server, existingZvol, subsystem, namespace, existingCapacity)
		if err != nil {
			timer.ObserveError()
			return nil, false, err
		}
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
	klog.V(4).Info("Creating NVMe-oF volume")

	// Validate and extract parameters
	params, err := validateNVMeOFParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating ZVOL: %s with size: %d bytes", params.zvolName, params.requestedCapacity)

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
		// If not done, ZVOL exists but no namespace - continue with namespace creation
	}

	// Verify pre-configured subsystem exists
	klog.V(4).Infof(msgVerifyingNVMeOFSubsystem, params.subsystemNQN)
	subsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, params.subsystemNQN)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.FailedPrecondition, msgSubsystemNotFound, params.subsystemNQN, err)
	}

	klog.V(4).Infof("Found pre-configured NVMe-oF subsystem: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

	// Create or use existing ZVOL
	zvol, err := s.getOrCreateZVOL(ctx, params, existingZvols, timer)
	if err != nil {
		return nil, err
	}

	// Create NVMe-oF namespace
	namespace, err := s.createNVMeOFNamespaceForZVOL(ctx, zvol, subsystem, timer)
	if err != nil {
		return nil, err
	}

	// Build and return response
	resp, err := buildNVMeOFVolumeResponse(params.volumeName, params.server, zvol, subsystem, namespace, params.requestedCapacity)
	if err != nil {
		// Cleanup on failure
		s.cleanupNVMeOFResources(ctx, namespace.ID, zvol.ID)
		timer.ObserveError()
		return nil, err
	}

	klog.Infof("Created NVMe-oF volume: %s", params.volumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// getOrCreateZVOL gets an existing ZVOL or creates a new one.
func (s *ControllerService) getOrCreateZVOL(ctx context.Context, params *nvmeofVolumeParams, existingZvols []tnsapi.Dataset, timer *metrics.OperationTimer) (*tnsapi.Dataset, error) {
	if len(existingZvols) > 0 {
		zvol := &existingZvols[0]
		klog.V(4).Infof("Using existing ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
		return zvol, nil
	}

	// Create new ZVOL
	zvol, err := s.apiClient.CreateZvol(ctx, tnsapi.ZvolCreateParams{
		Name:         params.zvolName,
		Type:         "VOLUME",
		Volsize:      params.requestedCapacity,
		Volblocksize: "16K", // Default block size for NVMe-oF
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", err)
	}

	klog.V(4).Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
	return zvol, nil
}

// createNVMeOFNamespaceForZVOL creates an NVMe-oF namespace for a ZVOL.
func (s *ControllerService) createNVMeOFNamespaceForZVOL(ctx context.Context, zvol *tnsapi.Dataset, subsystem *tnsapi.NVMeOFSubsystem, timer *metrics.OperationTimer) (*tnsapi.NVMeOFNamespace, error) {
	devicePath := "zvol/" + zvol.Name

	klog.V(4).Infof("Creating NVMe-oF namespace for device: %s in subsystem %d (ZVOL ID: %s)", devicePath, subsystem.ID, zvol.ID)

	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
	})
	if err != nil {
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.V(4).Infof("Created NVMe-oF namespace: ID=%d, NSID=%d, device=%s, subsystem=%d, ZVOL=%s",
		namespace.ID, namespace.NSID, devicePath, subsystem.ID, zvol.ID)
	return namespace, nil
}

// cleanupNVMeOFResources cleans up NVMe-oF namespace and ZVOL on failure.
func (s *ControllerService) cleanupNVMeOFResources(ctx context.Context, namespaceID int, zvolID string) {
	klog.Errorf("Cleaning up NVMe-oF resources due to failure")
	if namespaceID > 0 {
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespaceID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
	}
	if zvolID != "" {
		if delErr := s.apiClient.DeleteDataset(ctx, zvolID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
	}
}

// deleteNVMeOFVolume deletes an NVMe-oF volume.
// NOTE: This function does NOT delete the NVMe-oF subsystem. Subsystems are pre-configured
// infrastructure that serve multiple volumes (namespaces). Only the namespace and ZVOL are deleted.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "delete")
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, namespace ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFNamespaceID)

	// Step 1: Delete NVMe-oF namespace
	if err := s.deleteNVMeOFNamespace(ctx, meta, timer); err != nil {
		return nil, err
	}

	// Step 2: Delete ZVOL
	if err := s.deleteZVOL(ctx, meta); err != nil {
		return nil, err
	}

	// NOTE: Subsystem (ID: %d) is NOT deleted - it's pre-configured infrastructure
	// serving multiple volumes. Administrator manages subsystem lifecycle independently.
	if meta.NVMeOFSubsystemID > 0 {
		klog.V(4).Infof("Subsystem ID %d is preserved (pre-configured infrastructure, not volume-specific)", meta.NVMeOFSubsystemID)
	}

	klog.Infof("Deleted NVMe-oF volume: %s (namespace and ZVOL only)", meta.Name)

	// Remove volume capacity metric
	// Note: We need to reconstruct the volumeID to delete the metric
	if encodedVolumeID, err := encodeVolumeID(*meta); err == nil {
		metrics.DeleteVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF)
	}

	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// deleteNVMeOFNamespace deletes an NVMe-oF namespace and verifies deletion.
func (s *ControllerService) deleteNVMeOFNamespace(ctx context.Context, meta *VolumeMetadata, timer *metrics.OperationTimer) error {
	if meta.NVMeOFNamespaceID <= 0 {
		return nil
	}

	klog.V(4).Infof("Deleting NVMe-oF namespace: ID=%d, ZVOL=%s, dataset=%s",
		meta.NVMeOFNamespaceID, meta.DatasetID, meta.DatasetName)

	if err := s.apiClient.DeleteNVMeOFNamespace(ctx, meta.NVMeOFNamespaceID); err != nil {
		// Check if namespace already deleted (idempotency)
		if isNotFoundError(err) {
			klog.V(4).Infof("Namespace %d not found, assuming already deleted (idempotency)", meta.NVMeOFNamespaceID)
			return nil
		}
		// For other errors, fail and retry to prevent orphaned ZVOLs
		e := status.Errorf(codes.Internal, "Failed to delete NVMe-oF namespace %d (ZVOL: %s): %v",
			meta.NVMeOFNamespaceID, meta.DatasetID, err)
		klog.Error(e)
		timer.ObserveError()
		return e
	}

	klog.V(4).Infof("Deleted NVMe-oF namespace %d (ZVOL: %s)", meta.NVMeOFNamespaceID, meta.DatasetID)

	// Verify namespace is gone
	return s.verifyNamespaceDeletion(ctx, meta, timer)
}

// verifyNamespaceDeletion verifies that a namespace has been fully deleted.
func (s *ControllerService) verifyNamespaceDeletion(ctx context.Context, meta *VolumeMetadata, timer *metrics.OperationTimer) error {
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
			ns.ID, ns.NSID, ns.Device)
		klog.Error(e)
		timer.ObserveError()
		return e
	}

	klog.V(4).Infof("Verified namespace %d is fully deleted", meta.NVMeOFNamespaceID)
	return nil
}

// deleteZVOL deletes a ZVOL dataset.
func (s *ControllerService) deleteZVOL(ctx context.Context, meta *VolumeMetadata) error {
	if meta.DatasetID == "" {
		return nil
	}

	klog.V(4).Infof("Deleting ZVOL: %s", meta.DatasetID)
	if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
		// Check if dataset doesn't exist - this is OK (idempotency)
		if isNotFoundError(err) {
			klog.V(4).Infof("ZVOL %s not found, assuming already deleted (idempotency)", meta.DatasetID)
			return nil
		}
		// For other errors, return error to trigger retry and prevent orphaned ZVOLs
		return status.Errorf(codes.Internal, "Failed to delete ZVOL %s: %v", meta.DatasetID, err)
	}

	klog.V(4).Infof("Deleted ZVOL %s", meta.DatasetID)
	return nil
}

func (s *ControllerService) setupNVMeOFVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server, subsystemNQN, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NVMe-oF namespace for cloned ZVOL: %s", zvol.Name)

	volumeName := req.GetName()

	// Step 1: Verify pre-configured subsystem exists
	klog.V(4).Infof("Verifying NVMe-oF subsystem exists with NQN: %s", subsystemNQN)
	subsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, subsystemNQN)
	if err != nil {
		// Cleanup: delete the cloned ZVOL if subsystem verification fails
		klog.Errorf("Failed to find NVMe-oF subsystem, cleaning up cloned ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"Failed to find NVMe-oF subsystem with NQN '%s'. "+
				"Pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
				"with ports attached before provisioning volumes. Error: %v", subsystemNQN, err)
	}

	klog.V(4).Infof("Found pre-configured NVMe-oF subsystem: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

	// Step 2: Create NVMe-oF namespace within pre-configured subsystem
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvol.Name

	klog.V(4).Infof("Creating NVMe-oF namespace for device: %s in subsystem %d", devicePath, subsystem.ID)

	// Note: NSID is not specified (omitted) - TrueNAS will auto-assign the next available namespace ID
	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		// NSID is omitted - TrueNAS auto-assigns next available ID
	})
	if err != nil {
		// Cleanup: delete cloned ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up cloned ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.V(4).Infof("Created NVMe-oF namespace with ID: %d (NSID: %d)", namespace.ID, namespace.NSID)

	// Get requested capacity (needed before creating metadata)
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
		SubsystemNQN:      subsystemNQN,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete namespace and cloned ZVOL (do NOT delete subsystem)
		klog.Errorf("Failed to encode volume ID for cloned volume, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespace.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID for cloned volume: %v", err)
	}

	// Construct volume context with metadata for node plugin
	volumeContext := map[string]string{
		"server":            server,
		"nqn":               subsystem.NQN,
		"datasetID":         zvol.ID,
		"datasetName":       zvol.Name,
		"nvmeofSubsystemID": strconv.Itoa(subsystem.ID),
		"nvmeofNamespaceID": strconv.Itoa(namespace.ID),
		"nsid":              strconv.Itoa(namespace.NSID),
	}

	// Include expected capacity for device verification during staging
	volumeContext["expectedCapacity"] = strconv.FormatInt(requestedCapacity, 10)

	// CRITICAL: Mark this volume as cloned from snapshot in VolumeContext
	// This signals to the node that the volume has existing data and should NEVER be formatted
	volumeContext["clonedFromSnapshot"] = "true"

	klog.Infof("Created NVMe-oF volume from snapshot: %s", volumeName)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF, requestedCapacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
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

	// Update volume capacity metric
	// Note: We need to reconstruct the volumeID to update the metric
	if encodedVolumeID, err := encodeVolumeID(*meta); err == nil {
		metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF, requiredBytes)
	}

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: true, // NVMe-oF volumes require node-side filesystem expansion
	}, nil
}
