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

//nolint:gocognit,gocyclo,nestif // Complexity from idempotency checks and error handling - architectural requirement
func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("nvmeof", "create")
	klog.V(4).Info("Creating NVMe-oF volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NVMe-oF volumes")
	}

	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	subsystemNQN := params["subsystemNQN"]
	if subsystemNQN == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument,
			"subsystemNQN parameter is required for NVMe-oF volumes. "+
				"Pre-configure an NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
				"and provide its NQN in the StorageClass parameters.")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Construct ZVOL name (parent/volumeID)
	volumeName := req.GetName()
	zvolName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	klog.Infof("Creating ZVOL: %s with size: %d bytes", zvolName, requestedCapacity)

	// Check if ZVOL already exists (idempotency)
	existingZvols, err := s.apiClient.QueryAllDatasets(ctx, zvolName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing ZVOLs: %v", err)
	}

	// If ZVOL exists, check if it matches the request
	if len(existingZvols) > 0 {
		existingZvol := existingZvols[0]
		klog.Infof("ZVOL %s already exists (ID: %s), checking idempotency", zvolName, existingZvol.ID)

		// Extract existing ZVOL capacity
		existingCapacity := getZvolCapacity(&existingZvol)
		if existingCapacity > 0 {
			klog.Infof("Existing ZVOL capacity: %d bytes, requested: %d bytes", existingCapacity, requestedCapacity)

			// Check if capacity matches (CSI idempotency requirement)
			if existingCapacity != requestedCapacity {
				timer.ObserveError()
				return nil, status.Errorf(codes.AlreadyExists,
					"Volume '%s' already exists with different capacity: existing=%d bytes, requested=%d bytes",
					volumeName, existingCapacity, requestedCapacity)
			}
		} else {
			// If we can't determine capacity, assume compatible (backward compatibility)
			klog.Warningf("Could not determine capacity for existing ZVOL %s, assuming compatible", zvolName)
			existingCapacity = requestedCapacity
		}

		// Verify subsystem exists
		klog.Infof("Verifying NVMe-oF subsystem exists with NQN: %s", subsystemNQN)
		subsystem, subsysErr := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, subsystemNQN)
		if subsysErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.FailedPrecondition,
				"Failed to find NVMe-oF subsystem with NQN '%s'. "+
					"Pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
					"with ports attached before provisioning volumes. Error: %v", subsystemNQN, subsysErr)
		}

		// Check if namespace already exists for this ZVOL
		devicePath := "zvol/" + zvolName
		namespaces, nsErr := s.apiClient.QueryAllNVMeOFNamespaces(ctx)
		if nsErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to query NVMe-oF namespaces: %v", nsErr)
		}

		klog.V(4).Infof("Checking for existing namespace: device=%s, subsystem=%d, total namespaces=%d", devicePath, subsystem.ID, len(namespaces))

		// Log all namespaces for this subsystem to help diagnose NSID conflicts
		subsystemNamespaces := 0
		for _, ns := range namespaces {
			if ns.Subsystem == subsystem.ID {
				subsystemNamespaces++
				klog.V(5).Infof("Existing namespace in subsystem %d: ID=%d, NSID=%d, device=%s",
					subsystem.ID, ns.ID, ns.NSID, ns.Device)
			}
		}
		if subsystemNamespaces > 0 {
			klog.V(4).Infof("Found %d existing namespace(s) in subsystem %d", subsystemNamespaces, subsystem.ID)
		}

		// Find namespace matching this ZVOL in the target subsystem
		for _, ns := range namespaces {
			if ns.Subsystem != subsystem.ID || ns.Device != devicePath {
				continue
			}
			// Volume already exists with namespace - return existing volume
			klog.Infof("NVMe-oF volume already exists (namespace ID: %d, NSID: %d, device: %s, subsystem: %d), returning existing volume",
				ns.ID, ns.NSID, ns.Device, ns.Subsystem)

			meta := VolumeMetadata{
				Name:              volumeName,
				Protocol:          "nvmeof",
				DatasetID:         existingZvol.ID,
				DatasetName:       existingZvol.Name,
				Server:            server,
				NVMeOFSubsystemID: subsystem.ID,
				NVMeOFNamespaceID: ns.ID,
				NVMeOFNQN:         subsystem.NQN,
				SubsystemNQN:      subsystem.NQN,
			}

			encodedVolumeID, encodeErr := encodeVolumeID(meta)
			if encodeErr != nil {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to encode existing volume ID: %v", encodeErr)
			}

			volumeContext := map[string]string{
				"server":            server,
				"nqn":               subsystem.NQN,
				"datasetID":         existingZvol.ID,
				"datasetName":       existingZvol.Name,
				"nvmeofSubsystemID": strconv.Itoa(subsystem.ID),
				"nvmeofNamespaceID": strconv.Itoa(ns.ID),
				"nsid":              strconv.Itoa(ns.NSID),
			}

			// Record volume capacity metric (use EXISTING capacity, not requested)
			metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF, existingCapacity)

			timer.ObserveSuccess()
			return &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId:      encodedVolumeID,
					CapacityBytes: existingCapacity,
					VolumeContext: volumeContext,
				},
			}, nil
		}
		// If ZVOL exists but no namespace, we'll create the namespace below
		// (This handles partial creation scenarios)
	}

	// Step 1: Verify pre-configured subsystem exists
	klog.Infof("Verifying NVMe-oF subsystem exists with NQN: %s", subsystemNQN)
	subsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, subsystemNQN)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.FailedPrecondition,
			"Failed to find NVMe-oF subsystem with NQN '%s'. "+
				"Pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems) "+
				"with ports attached before provisioning volumes. Error: %v", subsystemNQN, err)
	}

	klog.Infof("Found pre-configured NVMe-oF subsystem: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

	// Step 2: Create ZVOL (block device) or use existing if already created
	var zvol *tnsapi.Dataset
	if len(existingZvols) > 0 {
		// ZVOL exists but no namespace - use existing ZVOL
		zvol = &existingZvols[0]
		klog.Infof("Using existing ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
	} else {
		// Create new ZVOL
		newZvol, createErr := s.apiClient.CreateZvol(ctx, tnsapi.ZvolCreateParams{
			Name:         zvolName,
			Type:         "VOLUME",
			Volsize:      requestedCapacity,
			Volblocksize: "16K", // Default block size for NVMe-oF
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", createErr)
		}
		zvol = newZvol
		klog.Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)
	}

	// Step 3: Create NVMe-oF namespace within pre-configured subsystem
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvolName

	klog.Infof("Creating NVMe-oF namespace for device: %s in subsystem %d (ZVOL ID: %s)", devicePath, subsystem.ID, zvol.ID)

	// Note: NSID is not specified (omitted) - TrueNAS will auto-assign the next available namespace ID
	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		// NSID is omitted - TrueNAS auto-assigns next available ID
	})
	if err != nil {
		// Cleanup: delete the ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF namespace: %v", err)
	}

	klog.Infof("Successfully created NVMe-oF namespace: ID=%d, NSID=%d, device=%s, subsystem=%d, ZVOL=%s",
		namespace.ID, namespace.NSID, devicePath, subsystem.ID, zvol.ID)

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          "nvmeof",
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
		// Cleanup: delete namespace and ZVOL (do NOT delete subsystem)
		klog.Errorf("Failed to encode volume ID, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespace.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID: %v", err)
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

	klog.Infof("Successfully created NVMe-oF volume with encoded ID: %s", encodedVolumeID)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF, requestedCapacity)

	timer.ObserveSuccess()
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// deleteNVMeOFVolume deletes an NVMe-oF volume.
// NOTE: This function does NOT delete the NVMe-oF subsystem. Subsystems are pre-configured
// infrastructure that serve multiple volumes (namespaces). Only the namespace and ZVOL are deleted.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("nvmeof", "delete")
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, namespace ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFNamespaceID)

	// Step 1: Delete NVMe-oF namespace
	if meta.NVMeOFNamespaceID > 0 {
		klog.Infof("Deleting NVMe-oF namespace: ID=%d, ZVOL=%s, dataset=%s",
			meta.NVMeOFNamespaceID, meta.DatasetID, meta.DatasetName)
		if err := s.apiClient.DeleteNVMeOFNamespace(ctx, meta.NVMeOFNamespaceID); err != nil {
			// Log error but continue - the namespace might already be deleted
			klog.Warningf("Failed to delete NVMe-oF namespace %d (ZVOL: %s): %v (continuing anyway)",
				meta.NVMeOFNamespaceID, meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted NVMe-oF namespace %d (ZVOL: %s)", meta.NVMeOFNamespaceID, meta.DatasetID)

			// Verify namespace is gone by querying all namespaces
			// This helps ensure TrueNAS has completed the deletion before we return
			klog.V(4).Infof("Verifying namespace %d deletion...", meta.NVMeOFNamespaceID)
			if allNamespaces, queryErr := s.apiClient.QueryAllNVMeOFNamespaces(ctx); queryErr == nil {
				found := false
				for _, ns := range allNamespaces {
					if ns.ID == meta.NVMeOFNamespaceID {
						found = true
						klog.Warningf("Namespace %d still exists after deletion (NSID: %d, device: %s) - may indicate async deletion",
							ns.ID, ns.NSID, ns.Device)
						break
					}
				}
				if !found {
					klog.V(4).Infof("Verified namespace %d is fully deleted", meta.NVMeOFNamespaceID)
				}
			}
		}
	}

	// Step 2: Delete ZVOL
	if meta.DatasetID != "" {
		klog.Infof("Deleting ZVOL: %s", meta.DatasetID)
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			// Check if dataset doesn't exist - this is OK (idempotency)
			klog.Warningf("Failed to delete ZVOL %s: %v (continuing anyway)", meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted ZVOL %s", meta.DatasetID)
		}
	}

	// NOTE: Subsystem (ID: %d) is NOT deleted - it's pre-configured infrastructure
	// serving multiple volumes. Administrator manages subsystem lifecycle independently.
	if meta.NVMeOFSubsystemID > 0 {
		klog.V(4).Infof("Subsystem ID %d is preserved (pre-configured infrastructure, not volume-specific)", meta.NVMeOFSubsystemID)
	}

	klog.Infof("Successfully deleted NVMe-oF volume: %s (namespace and ZVOL only)", meta.Name)

	// Remove volume capacity metric
	// Note: We need to reconstruct the volumeID to delete the metric
	if encodedVolumeID, err := encodeVolumeID(*meta); err == nil {
		metrics.DeleteVolumeCapacity(encodedVolumeID, metrics.ProtocolNVMeOF)
	}

	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *ControllerService) setupNVMeOFVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server, subsystemNQN, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NVMe-oF namespace for cloned ZVOL: %s", zvol.Name)

	volumeName := req.GetName()

	// Step 1: Verify pre-configured subsystem exists
	klog.Infof("Verifying NVMe-oF subsystem exists with NQN: %s", subsystemNQN)
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

	klog.Infof("Found pre-configured NVMe-oF subsystem: ID=%d, NQN=%s", subsystem.ID, subsystem.NQN)

	// Step 2: Create NVMe-oF namespace within pre-configured subsystem
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvol.Name

	klog.Infof("Creating NVMe-oF namespace for device: %s in subsystem %d", devicePath, subsystem.ID)

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

	klog.Infof("Created NVMe-oF namespace with ID: %d (NSID: %d)", namespace.ID, namespace.NSID)

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:              volumeName,
		Protocol:          "nvmeof",
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

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// CRITICAL: Mark this volume as cloned from snapshot in VolumeContext
	// This signals to the node that the volume has existing data and should NEVER be formatted
	volumeContext["clonedFromSnapshot"] = "true"

	klog.Infof("Successfully created NVMe-oF volume from snapshot with encoded ID: %s", encodedVolumeID)

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
	timer := metrics.NewVolumeOperationTimer("nvmeof", "expand")
	klog.V(4).Infof("Expanding NVMe-oF volume: %s (ZVOL: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	// For NVMe-oF volumes (ZVOLs), we update the volsize property
	klog.Infof("Expanding NVMe-oF ZVOL - DatasetID: %s, DatasetName: %s, New Size: %d bytes",
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

	klog.Infof("Successfully expanded NVMe-oF volume %s to %d bytes", meta.Name, requiredBytes)

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
