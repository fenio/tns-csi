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
	msgFailedCleanupClonedZVOL = "Failed to cleanup cloned ZVOL: %v"
	// NQN prefix for CSI-managed subsystems.
	// Format: nqn.2137.csi.tns:<volume-name>
	// Each volume gets its own subsystem with NSID=1 (independent subsystem architecture).
	nqnPrefix = "nqn.2137.csi.tns"
)

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
}

// generateNQN creates a unique NQN for a volume's dedicated subsystem.
// Format: nqn.2137.csi.tns:<volume-name>.
func generateNQN(volumeName string) string {
	return fmt.Sprintf("%s:%s", nqnPrefix, volumeName)
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

	volumeName := req.GetName()
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

	return &nvmeofVolumeParams{
		pool:              pool,
		server:            server,
		parentDataset:     parentDataset,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		zvolName:          zvolName,
		subsystemNQN:      subsystemNQN,
		portID:            portID,
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
		klog.V(4).Infof("Using first available NVMe-oF port: ID=%d", portID)
	}

	klog.V(4).Infof("Binding subsystem %d to port %d", subsystemID, portID)
	if err := s.apiClient.AddSubsystemToPort(ctx, subsystemID, portID); err != nil {
		timer.ObserveError()
		return status.Errorf(codes.Internal, "Failed to bind subsystem to port: %v", err)
	}

	klog.V(4).Infof("Successfully bound subsystem %d to port %d", subsystemID, portID)
	return nil
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

// deleteNVMeOFVolume deletes an NVMe-oF volume.
// With independent subsystem architecture, this deletes the namespace, subsystem, and ZVOL.
// Uses best-effort cleanup: continues deleting resources even if earlier steps fail.
// This prevents orphaned resources on TrueNAS when partial failures occur.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "delete")
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, namespace ID: %d, subsystem ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFNamespaceID, meta.NVMeOFSubsystemID)

	// Track all errors but continue with best-effort cleanup
	var errors []error

	// Step 1: Delete NVMe-oF namespace (best effort)
	if err := s.deleteNVMeOFNamespace(ctx, meta, timer); err != nil {
		klog.Errorf("Failed to delete namespace %d (continuing with cleanup): %v", meta.NVMeOFNamespaceID, err)
		errors = append(errors, fmt.Errorf("namespace deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted namespace %d", meta.NVMeOFNamespaceID)
	}

	// Step 2: Delete NVMe-oF subsystem (best effort - independent subsystem architecture)
	if err := s.deleteNVMeOFSubsystem(ctx, meta, timer); err != nil {
		klog.Errorf("Failed to delete subsystem %d (continuing with cleanup): %v", meta.NVMeOFSubsystemID, err)
		errors = append(errors, fmt.Errorf("subsystem deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted subsystem %d", meta.NVMeOFSubsystemID)
	}

	// Step 3: Delete ZVOL (best effort)
	if err := s.deleteZVOL(ctx, meta); err != nil {
		klog.Errorf("Failed to delete ZVOL %s (continuing with cleanup): %v", meta.DatasetID, err)
		errors = append(errors, fmt.Errorf("ZVOL deletion failed: %w", err))
	} else {
		klog.V(4).Infof("Successfully deleted ZVOL %s", meta.DatasetID)
	}

	// Evaluate cleanup results
	if len(errors) == 0 {
		// Complete success - all resources deleted
		klog.Infof("Deleted NVMe-oF volume: %s (namespace, subsystem, and ZVOL)", meta.Name)
		metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Partial or complete failure - return error to trigger retry
	// This prevents orphaned resources on TrueNAS by ensuring Kubernetes retries until all resources are cleaned
	klog.Errorf("Failed to delete %d of 3 resources for volume %s: %v", len(errors), meta.Name, errors)
	klog.Infof("Successfully deleted %d of 3 resources (namespace, subsystem, ZVOL) - will retry remaining", 3-len(errors))
	timer.ObserveError()
	return nil, status.Errorf(codes.Internal,
		"Failed to delete %d volume resources for %s (deleted %d): %v",
		len(errors), meta.Name, 3-len(errors), errors)
}

// deleteNVMeOFSubsystem deletes an NVMe-oF subsystem.
// This function first unbinds the subsystem from all ports before deletion
// to prevent orphaned subsystems due to TrueNAS refusing to delete subsystems with active port bindings.
func (s *ControllerService) deleteNVMeOFSubsystem(ctx context.Context, meta *VolumeMetadata, timer *metrics.OperationTimer) error {
	if meta.NVMeOFSubsystemID <= 0 {
		return nil
	}

	klog.V(4).Infof("Deleting NVMe-oF subsystem: ID=%d", meta.NVMeOFSubsystemID)

	// Step 1: Query and unbind all port associations first
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

	// Step 2: Delete the subsystem
	if err := s.apiClient.DeleteNVMeOFSubsystem(ctx, meta.NVMeOFSubsystemID); err != nil {
		// Check if subsystem already deleted (idempotency)
		if isNotFoundError(err) {
			klog.V(4).Infof("Subsystem %d not found, assuming already deleted (idempotency)", meta.NVMeOFSubsystemID)
			return nil
		}
		// For other errors, fail and retry
		e := status.Errorf(codes.Internal, "Failed to delete NVMe-oF subsystem %d: %v",
			meta.NVMeOFSubsystemID, err)
		klog.Error(e)
		timer.ObserveError()
		return e
	}

	klog.V(4).Infof("Deleted NVMe-oF subsystem %d", meta.NVMeOFSubsystemID)
	return nil
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

// setupNVMeOFVolumeFromClone sets up NVMe-oF infrastructure for a cloned ZVOL.
// With independent subsystem architecture, creates a new subsystem for the clone.
func (s *ControllerService) setupNVMeOFVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server, _, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NVMe-oF namespace for cloned ZVOL: %s", zvol.Name)

	volumeName := req.GetName()
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "clone")

	// Generate NQN for the cloned volume's dedicated subsystem
	subsystemNQN := generateNQN(volumeName)

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
	klog.V(4).Infof("Creating dedicated NVMe-oF subsystem for clone: %s", subsystemNQN)
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

	klog.V(4).Infof("Created NVMe-oF subsystem: ID=%d, Name=%s", subsystem.ID, subsystem.Name)

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
	klog.V(4).Infof("Creating NVMe-oF namespace for device: %s in subsystem %d", devicePath, subsystem.ID)

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

	klog.V(4).Infof("Created NVMe-oF namespace: ID=%d, NSID=%d", namespace.ID, namespace.NSID)

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Build volume metadata
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          ProtocolNVMeOF,
		DatasetID:         zvol.ID,
		DatasetName:       zvol.Name,
		Server:            server,
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystemNQN, // Use the NQN we generated, not subsystem.Name
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

	klog.Infof("Created NVMe-oF volume from snapshot: %s (subsystem: %s, NSID: 1)", volumeName, subsystemNQN)

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
