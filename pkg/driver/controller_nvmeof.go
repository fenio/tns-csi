// Package driver implements NVMe-oF-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

//nolint:gocognit,gocyclo // Complex NVMe-oF provisioning logic - refactoring would risk stability of working code
func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating NVMe-oF volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NVMe-oF volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
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

	// Step 1: Create ZVOL (block device)
	zvol, err := s.apiClient.CreateZvol(ctx, tnsapi.ZvolCreateParams{
		Name:         zvolName,
		Type:         "VOLUME",
		Volsize:      requestedCapacity,
		Volblocksize: "16K", // Default block size for NVMe-oF
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL: %v", err)
	}

	klog.Infof("Created ZVOL: %s (ID: %s)", zvol.Name, zvol.ID)

	klog.Infof("Creating NVMe-oF subsystem with name: %s", volumeName)

	// Step 2: Create NVMe-oF subsystem with allow_any_host enabled
	// Use volume name as the subsystem name (TrueNAS will auto-generate NQN)
	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
		Name:         volumeName,
		AllowAnyHost: true, // Allow any host to connect without explicit host NQN registration
	})
	if err != nil {
		// Cleanup: delete the ZVOL if subsystem creation fails
		klog.Errorf("Failed to create NVMe-oF subsystem, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL after subsystem creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem: %v", err)
	}

	klog.Infof("Created NVMe-oF subsystem with ID: %d", subsystem.ID)

	// Step 2b: Query for available NVMe-oF ports and attach subsystem to the TCP port
	ports, err := s.apiClient.QueryNVMeOFPorts(ctx)
	if err != nil {
		klog.Errorf("Failed to query NVMe-oF ports, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to query NVMe-oF ports: %v", err)
	}

	// Find the TCP port (typically port 4420)
	var portID int
	for _, port := range ports {
		if port.Transport == "TCP" {
			portID = port.ID
			klog.Infof("Found NVMe-oF TCP port %d at %s:%d", port.ID, port.Address, port.Port)
			break
		}
	}

	if portID == 0 {
		klog.Errorf("No TCP NVMe-oF port found on TrueNAS server. NVMe-oF ports must be pre-configured before provisioning volumes.")
		klog.Infof("To configure NVMe-oF: In TrueNAS 25.10+ UI, go to Shares > NVMe-oF Subsystems, create or edit a subsystem, and add a TCP port (default: 4420)")
		klog.Infof("Cleaning up subsystem and ZVOL...")
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, status.Error(codes.FailedPrecondition,
			"No TCP NVMe-oF port configured on TrueNAS server. Please configure an NVMe-oF TCP port in TrueNAS 25.10+ (Shares > NVMe-oF Subsystems > Add Port) before provisioning volumes.")
	}

	// Attach subsystem to the port
	klog.Infof("Attaching subsystem %d to port %d", subsystem.ID, portID)
	if attachErr := s.apiClient.AddSubsystemToPort(ctx, subsystem.ID, portID); attachErr != nil {
		klog.Errorf("Failed to attach subsystem to port, cleaning up: %v", attachErr)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to attach subsystem to port: %v", attachErr)
	}

	klog.Infof("Successfully attached subsystem %d to port %d", subsystem.ID, portID)

	// Step 3: Create NVMe-oF namespace
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvolName

	klog.Infof("Creating NVMe-oF namespace for device: %s", devicePath)

	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		NSID:       1, // Start with namespace ID 1
	})
	if err != nil {
		// Cleanup: delete subsystem and ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
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
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete namespace, subsystem, and ZVOL
		klog.Errorf("Failed to encode volume ID, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespace.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
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

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// deleteNVMeOFVolume deletes an NVMe-oF volume.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("Deleting NVMe-oF volume: %s (dataset: %s, subsystem ID: %d, namespace ID: %d)",
		meta.Name, meta.DatasetName, meta.NVMeOFSubsystemID, meta.NVMeOFNamespaceID)

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

	// Step 2: Delete NVMe-oF subsystem
	if meta.NVMeOFSubsystemID > 0 {
		klog.Infof("Deleting NVMe-oF subsystem with ID: %d", meta.NVMeOFSubsystemID)
		if err := s.apiClient.DeleteNVMeOFSubsystem(ctx, meta.NVMeOFSubsystemID); err != nil {
			// Log error but continue - the subsystem might already be deleted
			klog.Warningf("Failed to delete NVMe-oF subsystem %d: %v (continuing anyway)", meta.NVMeOFSubsystemID, err)
		} else {
			klog.Infof("Successfully deleted NVMe-oF subsystem %d", meta.NVMeOFSubsystemID)
		}
	}

	// Step 3: Delete ZVOL
	if meta.DatasetID != "" {
		klog.Infof("Deleting ZVOL: %s", meta.DatasetID)
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			// Check if dataset doesn't exist - this is OK (idempotency)
			klog.Warningf("Failed to delete ZVOL %s: %v (continuing anyway)", meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted ZVOL %s", meta.DatasetID)
		}
	}

	klog.Infof("Successfully deleted NVMe-oF volume: %s", meta.Name)
	return &csi.DeleteVolumeResponse{}, nil
}

//nolint:gocognit,gocyclo // Complex NVMe-oF setup logic - refactoring would risk stability of working code
func (s *ControllerService) setupNVMeOFVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NVMe-oF subsystem and namespace for cloned ZVOL: %s", zvol.Name)

	volumeName := req.GetName()

	// Step 1: Create NVMe-oF subsystem with allow_any_host enabled
	klog.Infof("Creating NVMe-oF subsystem with name: %s", volumeName)

	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
		Name:         volumeName,
		AllowAnyHost: true, // Allow any host to connect without explicit host NQN registration
	})
	if err != nil {
		// Cleanup: delete the cloned ZVOL if subsystem creation fails
		klog.Errorf("Failed to create NVMe-oF subsystem for cloned volume, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL after subsystem creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem for cloned volume: %v", err)
	}

	klog.Infof("Created NVMe-oF subsystem with ID: %d", subsystem.ID)

	// Step 2: Query for available NVMe-oF ports and attach subsystem to the TCP port
	ports, err := s.apiClient.QueryNVMeOFPorts(ctx)
	if err != nil {
		klog.Errorf("Failed to query NVMe-oF ports, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to query NVMe-oF ports: %v", err)
	}

	// Find the TCP port (typically port 4420)
	var portID int
	for _, port := range ports {
		if port.Transport == "TCP" {
			portID = port.ID
			klog.Infof("Found NVMe-oF TCP port %d at %s:%d", port.ID, port.Address, port.Port)
			break
		}
	}

	if portID == 0 {
		klog.Errorf("No TCP NVMe-oF port found on TrueNAS server.")
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Error(codes.FailedPrecondition,
			"No TCP NVMe-oF port configured on TrueNAS server. Please configure an NVMe-oF TCP port in TrueNAS.")
	}

	// Attach subsystem to the port
	klog.Infof("Attaching subsystem %d to port %d", subsystem.ID, portID)
	if attachErr := s.apiClient.AddSubsystemToPort(ctx, subsystem.ID, portID); attachErr != nil {
		klog.Errorf("Failed to attach subsystem to port, cleaning up: %v", attachErr)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to attach subsystem to port: %v", attachErr)
	}

	klog.Infof("Successfully attached subsystem %d to port %d", subsystem.ID, portID)

	// Step 3: Create NVMe-oF namespace
	// Device path should be zvol/<dataset-name> (without /dev/ prefix)
	devicePath := "zvol/" + zvol.Name

	klog.Infof("Creating NVMe-oF namespace for device: %s", devicePath)

	namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, tnsapi.NVMeOFNamespaceCreateParams{
		SubsysID:   subsystem.ID,
		DevicePath: devicePath,
		DeviceType: "ZVOL",
		NSID:       1, // Start with namespace ID 1
	})
	if err != nil {
		// Cleanup: delete subsystem and ZVOL
		klog.Errorf("Failed to create NVMe-oF namespace, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
		}
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
		NVMeOFSubsystemID: subsystem.ID,
		NVMeOFNamespaceID: namespace.ID,
		NVMeOFNQN:         subsystem.NQN,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete namespace, subsystem, and ZVOL
		klog.Errorf("Failed to encode volume ID for cloned volume, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNVMeOFNamespace(ctx, namespace.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF namespace: %v", delErr)
		}
		if delErr := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystem.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NVMe-oF subsystem: %v", delErr)
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

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
			ContentSource: &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Snapshot{
					Snapshot: &csi.VolumeContentSource_SnapshotSource{
						SnapshotId: req.GetVolumeContentSource().GetSnapshot().GetSnapshotId(),
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
	klog.V(4).Infof("Expanding NVMe-oF volume: %s (ZVOL: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
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
		return nil, status.Errorf(codes.Internal,
			"Failed to update ZVOL size for dataset '%s' (Name: '%s'). "+
				"The dataset may not exist on TrueNAS - verify it exists at Storage > Pools. "+
				"Error: %v", meta.DatasetID, meta.DatasetName, err)
	}

	klog.Infof("Successfully expanded NVMe-oF volume %s to %d bytes", meta.Name, requiredBytes)

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: true, // NVMe-oF volumes require node-side filesystem expansion
	}, nil
}
