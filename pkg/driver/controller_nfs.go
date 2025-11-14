// Package driver implements NFS-specific CSI controller operations.
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

// createNFSVolume creates an NFS volume with a ZFS dataset and NFS share.
//
//nolint:gocognit,gocyclo,nestif // Complexity from idempotency checks and error handling - architectural requirement
func (s *ControllerService) createNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("nfs", "create")

	klog.V(4).Info("Creating NFS volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NFS volumes")
	}

	// Server parameter - optional for testing with default value
	// In production, this should be the TrueNAS server IP/hostname
	server := params["server"]
	if server == "" {
		server = "truenas.local" // Default for testing
		klog.V(4).Info("No server parameter provided, using default: truenas.local")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Construct dataset name (parent/volumeID)
	volumeID := req.GetName()
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeID)

	// Get requested capacity (needed for both creation and idempotency)
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	klog.Infof("Creating dataset: %s with capacity: %d bytes", datasetName, requestedCapacity)

	// Check if dataset already exists (idempotency)
	existingDatasets, err := s.apiClient.QueryAllDatasets(ctx, datasetName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing datasets: %v", err)
	}

	// If dataset exists, check if it matches the request
	if len(existingDatasets) > 0 {
		existingDataset := existingDatasets[0]
		klog.Infof("Dataset %s already exists (ID: %s), checking idempotency", datasetName, existingDataset.ID)

		// Check if an NFS share exists for this dataset
		existingShares, shareErr := s.apiClient.QueryAllNFSShares(ctx, existingDataset.Mountpoint)
		if shareErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to query existing NFS shares: %v", shareErr)
		}

		if len(existingShares) > 0 {
			// Volume already exists with NFS share - check if capacity matches
			existingShare := existingShares[0]
			klog.Infof("NFS volume already exists (share ID: %d), checking capacity compatibility", existingShare.ID)

			// Parse capacity from comment (format: "CSI Volume: name, Capacity: bytes")
			// If comment doesn't contain capacity, assume it matches (backward compatibility)
			var existingCapacity int64
			if existingShare.Comment != "" {
				var parsedCapacity int64
				_, scanErr := fmt.Sscanf(existingShare.Comment, "CSI Volume: %s, Capacity: %d", new(string), &parsedCapacity)
				if scanErr == nil {
					existingCapacity = parsedCapacity
				}
			}

			// CSI spec: return AlreadyExists if volume exists with incompatible capacity
			if existingCapacity > 0 && existingCapacity != requestedCapacity {
				klog.Warningf("Volume %s exists with different capacity (existing: %d, requested: %d)",
					volumeID, existingCapacity, requestedCapacity)
				timer.ObserveError()
				return nil, status.Errorf(codes.AlreadyExists,
					"Volume %s already exists with different capacity (existing: %d bytes, requested: %d bytes)",
					volumeID, existingCapacity, requestedCapacity)
			}

			klog.Infof("Capacity is compatible, returning existing volume")

			// Build volume metadata
			meta := VolumeMetadata{
				Name:        volumeID,
				Protocol:    ProtocolNFS,
				DatasetID:   existingDataset.ID,
				DatasetName: existingDataset.Name,
				Server:      server,
				NFSShareID:  existingShare.ID,
			}

			encodedVolumeID, encodeErr := encodeVolumeID(meta)
			if encodeErr != nil {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to encode existing volume ID: %v", encodeErr)
			}

			volumeContext := map[string]string{
				"server":      server,
				"share":       existingDataset.Mountpoint,
				"datasetID":   existingDataset.ID,
				"datasetName": existingDataset.Name,
				"nfsShareID":  strconv.Itoa(existingShare.ID),
			}

			// Record volume capacity metric
			// Use existingCapacity if available, otherwise use requestedCapacity (for backward compatibility)
			capacityToReturn := requestedCapacity
			if existingCapacity > 0 {
				capacityToReturn = existingCapacity
			}
			metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNFS, capacityToReturn)

			timer.ObserveSuccess()
			return &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId:      encodedVolumeID,
					CapacityBytes: capacityToReturn,
					VolumeContext: volumeContext,
				},
			}, nil
		}
		// If dataset exists but no NFS share, we'll create the share below
		// (This handles partial creation scenarios)
	}

	// Step 1: Create ZFS dataset (or use existing if already created)
	var dataset *tnsapi.Dataset
	if len(existingDatasets) > 0 {
		// Dataset exists but no NFS share - use existing dataset
		dataset = &existingDatasets[0]
		klog.Infof("Using existing dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)
	} else {
		// Create new dataset
		newDataset, createErr := s.apiClient.CreateDataset(ctx, tnsapi.DatasetCreateParams{
			Name: datasetName,
			Type: "FILESYSTEM",
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create dataset: %v", createErr)
		}
		dataset = newDataset
		klog.Infof("Created dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)
	}

	// Step 2: Create NFS share for the dataset
	// Store capacity in comment for idempotency checks
	volumeName := req.GetName()
	comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", volumeName, requestedCapacity)
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      comment,
		MaprootUser:  "root",
		MaprootGroup: "wheel",
		Enabled:      true,
	})
	if err != nil {
		// Cleanup: delete the dataset if NFS share creation fails
		klog.Errorf("Failed to create NFS share, cleaning up dataset: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset after NFS share creation failure: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share: %v", err)
	}

	klog.Infof("Created NFS share with ID: %d for path: %s", nfsShare.ID, nfsShare.Path)

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		NFSShareID:  nfsShare.ID,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete NFS share and dataset
		klog.Errorf("Failed to encode volume ID, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNFSShare(ctx, nfsShare.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NFS share: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID: %v", err)
	}

	// Construct volume context with metadata for node plugin
	volumeContext := map[string]string{
		"server":      server,
		"share":       dataset.Mountpoint,
		"datasetID":   dataset.ID,
		"datasetName": dataset.Name,
		"nfsShareID":  strconv.Itoa(nfsShare.ID),
	}

	klog.Infof("Successfully created NFS volume with encoded ID: %s", encodedVolumeID)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNFS, requestedCapacity)

	timer.ObserveSuccess()
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      encodedVolumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// deleteNFSVolume deletes an NFS volume.
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("nfs", "delete")
	klog.V(4).Infof("Deleting NFS volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	// Delete ZFS dataset - TrueNAS automatically deletes associated NFS shares
	// when the dataset is deleted, so we don't need to explicitly delete the share
	if meta.DatasetID == "" {
		klog.Infof("No dataset ID provided, skipping dataset deletion")
	} else {
		klog.Infof("Deleting dataset: %s (NFS share %d will be automatically removed)", meta.DatasetID, meta.NFSShareID)
		err := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
		if err != nil && !isNotFoundError(err) {
			// For non-idempotent errors, return error to trigger retry and prevent orphaned datasets
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to delete dataset %s: %v", meta.DatasetID, err)
		}
		if err == nil {
			klog.Infof("Successfully deleted dataset %s and associated NFS share %d", meta.DatasetID, meta.NFSShareID)
		} else {
			klog.Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
		}
	}

	klog.Infof("Successfully deleted NFS volume: %s", meta.Name)

	// Remove volume capacity metric
	// Note: We need to reconstruct the volumeID to delete the metric
	if encodedVolumeID, err := encodeVolumeID(*meta); err == nil {
		metrics.DeleteVolumeCapacity(encodedVolumeID, metrics.ProtocolNFS)
	}

	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// setupNFSVolumeFromClone sets up an NFS share for a cloned dataset.
func (s *ControllerService) setupNFSVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.Dataset, server, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NFS share for cloned dataset: %s", dataset.Name)

	volumeName := req.GetName()

	// Create NFS share for the cloned dataset
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      "CSI Volume (from snapshot): " + volumeName,
		MaprootUser:  "root",
		MaprootGroup: "wheel",
		Enabled:      true,
	})
	if err != nil {
		// Cleanup: delete the cloned dataset if NFS share creation fails
		klog.Errorf("Failed to create NFS share for cloned dataset, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset after NFS share creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share for cloned volume: %v", err)
	}

	klog.Infof("Created NFS share with ID: %d for cloned dataset path: %s", nfsShare.ID, nfsShare.Path)

	// Encode volume metadata into volumeID
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		NFSShareID:  nfsShare.ID,
	}

	encodedVolumeID, err := encodeVolumeID(meta)
	if err != nil {
		// Cleanup: delete NFS share and dataset
		klog.Errorf("Failed to encode volume ID for cloned volume, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteNFSShare(ctx, nfsShare.ID); delErr != nil {
			klog.Errorf("Failed to cleanup NFS share: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to encode volume ID for cloned volume: %v", err)
	}

	// Construct volume context with metadata for node plugin
	// CRITICAL: Add clonedFromSnapshot flag to prevent reformatting of cloned volumes
	// ZFS clones inherit filesystems from snapshots, but detection may fail due to caching
	volumeContext := map[string]string{
		"server":             server,
		"share":              dataset.Mountpoint,
		"datasetID":          dataset.ID,
		"datasetName":        dataset.Name,
		"nfsShareID":         strconv.Itoa(nfsShare.ID),
		"clonedFromSnapshot": "true",
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	klog.Infof("Successfully created NFS volume from snapshot with encoded ID: %s", encodedVolumeID)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNFS, requestedCapacity)

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

// expandNFSVolume expands an NFS volume by updating the dataset quota.
//
//nolint:dupl // Similar to expandNVMeOFVolume but with different parameters (Quota vs Volsize, NodeExpansionRequired)
func (s *ControllerService) expandNFSVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer("nfs", "expand")
	klog.V(4).Infof("Expanding NFS volume: %s (dataset: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	// For NFS volumes, we update the quota on the dataset
	// Note: ZFS datasets don't have a strict "size", but we can set a quota
	// to limit the maximum space usage
	klog.Infof("Expanding NFS dataset - DatasetID: %s, DatasetName: %s, New Quota: %d bytes",
		meta.DatasetID, meta.DatasetName, requiredBytes)

	updateParams := tnsapi.DatasetUpdateParams{
		Quota: &requiredBytes,
	}

	_, err := s.apiClient.UpdateDataset(ctx, meta.DatasetID, updateParams)
	if err != nil {
		// Provide detailed error information to help diagnose dataset issues
		klog.Errorf("Failed to update dataset quota for %s (Name: %s): %v", meta.DatasetID, meta.DatasetName, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update dataset quota for '%s' (Name: '%s'). "+
				"The dataset may not exist on TrueNAS - verify it exists at Storage > Pools. "+
				"Error: %v", meta.DatasetID, meta.DatasetName, err)
	}

	klog.Infof("Successfully expanded NFS volume %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric
	// Note: We need to reconstruct the volumeID to update the metric
	if encodedVolumeID, err := encodeVolumeID(*meta); err == nil {
		metrics.SetVolumeCapacity(encodedVolumeID, metrics.ProtocolNFS, requiredBytes)
	}

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false, // NFS volumes don't require node-side expansion
	}, nil
}
