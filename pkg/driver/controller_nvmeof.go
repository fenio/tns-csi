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

	// Step 2: Create ZVOL (block device)
	zvol, err := s.apiClient.CreateZvol(ctx, tnsapi.ZvolCreateParams{
		Name:         zvolName,
		Type:         "VOLUME",
		Volsize:      requestedCapacity,
		Volblocksize: "16K", // Default block size for NVMe-oF
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", err)
	}

	klog.Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)

	// Step 3: Create NVMe-oF namespace within pre-configured subsystem
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvolName

	klog.Infof("Creating NVMe-oF namespace for device: %s in subsystem %d", devicePath, subsystem.ID)

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
		klog.Infof("Deleting NVMe-oF namespace with ID: %d", meta.NVMeOFNamespaceID)
		if err := s.apiClient.DeleteNVMeOFNamespace(ctx, meta.NVMeOFNamespaceID); err != nil {
			// Log error but continue - the namespace might already be deleted
			klog.Warningf("Failed to delete NVMe-oF namespace %d: %v (continuing anyway)", meta.NVMeOFNamespaceID, err)
		} else {
			klog.Infof("Successfully deleted NVMe-oF namespace %d", meta.NVMeOFNamespaceID)
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
