// Package driver implements NFS-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// nfsVolumeParams holds validated parameters for NFS volume creation.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type nfsVolumeParams struct {
	requestedCapacity int64
	pool              string
	server            string
	parentDataset     string
	volumeName        string
	datasetName       string
	nfsMountOptions   string // Custom NFS mount options from StorageClass
}

// validateNFSParams validates and extracts NFS volume parameters from the request.
func validateNFSParams(req *csi.CreateVolumeRequest) (*nfsVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NFS volumes")
	}

	// Server parameter - optional for testing with default value
	server := params["server"]
	if server == "" {
		server = "truenas.local" // Default for testing
		klog.V(4).Info("No server parameter provided, using default: truenas.local")
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
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	// Extract custom NFS mount options (optional)
	nfsMountOptions := params[VolumeContextKeyNFSMountOptions]

	return &nfsVolumeParams{
		pool:              pool,
		server:            server,
		parentDataset:     parentDataset,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		datasetName:       datasetName,
		nfsMountOptions:   nfsMountOptions,
	}, nil
}

// parseCapacityFromComment parses the capacity from an NFS share comment.
// Returns 0 if capacity cannot be parsed (backward compatibility).
func parseCapacityFromComment(comment string) int64 {
	if comment == "" {
		return 0
	}
	var parsedCapacity int64
	_, err := fmt.Sscanf(comment, "CSI Volume: %s | Capacity: %d", new(string), &parsedCapacity)
	if err != nil {
		return 0
	}
	return parsedCapacity
}

// buildNFSVolumeResponse builds the CreateVolumeResponse for an NFS volume.
func buildNFSVolumeResponse(volumeName, server, nfsMountOptions string, dataset *tnsapi.Dataset, nfsShare *tnsapi.NFSShare, capacity int64) *csi.CreateVolumeResponse {
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		NFSShareID:  nfsShare.ID,
	}

	// Volume ID is now just the volume name (CSI spec compliant, max 128 bytes)
	volumeID := volumeName

	// Build volume context with all necessary metadata
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = dataset.Mountpoint

	// Pass through custom NFS mount options if specified
	if nfsMountOptions != "" {
		volumeContext[VolumeContextKeyNFSMountOptions] = nfsMountOptions
	}

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNFS, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// handleExistingNFSVolume handles the case when a dataset already exists (idempotency).
func (s *ControllerService) handleExistingNFSVolume(ctx context.Context, params *nfsVolumeParams, existingDataset *tnsapi.Dataset, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Dataset %s already exists (ID: %s), checking idempotency", params.datasetName, existingDataset.ID)

	// Check if an NFS share exists for this dataset
	existingShares, err := s.apiClient.QueryAllNFSShares(ctx, existingDataset.Mountpoint)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to query existing NFS shares: %v", err)
	}

	if len(existingShares) == 0 {
		// Dataset exists but no NFS share - continue with share creation
		return nil, false, nil
	}

	// Volume already exists with NFS share - check if capacity matches
	existingShare := existingShares[0]
	klog.V(4).Infof("NFS volume already exists (share ID: %d), checking capacity compatibility", existingShare.ID)

	existingCapacity := parseCapacityFromComment(existingShare.Comment)

	// CSI spec: return AlreadyExists if volume exists with incompatible capacity
	if existingCapacity > 0 && existingCapacity != params.requestedCapacity {
		klog.Warningf("Volume %s exists with different capacity (existing: %d, requested: %d)",
			params.volumeName, existingCapacity, params.requestedCapacity)
		timer.ObserveError()
		return nil, false, status.Errorf(codes.AlreadyExists,
			"Volume %s already exists with different capacity (existing: %d bytes, requested: %d bytes)",
			params.volumeName, existingCapacity, params.requestedCapacity)
	}

	klog.V(4).Infof("Capacity is compatible, returning existing volume")

	// Use existingCapacity if available, otherwise use requestedCapacity (for backward compatibility)
	capacityToReturn := params.requestedCapacity
	if existingCapacity > 0 {
		capacityToReturn = existingCapacity
	}

	resp := buildNFSVolumeResponse(params.volumeName, params.server, params.nfsMountOptions, existingDataset, &existingShare, capacityToReturn)

	timer.ObserveSuccess()
	return resp, true, nil
}

// getOrCreateDataset gets an existing dataset or creates a new one.
func (s *ControllerService) getOrCreateDataset(ctx context.Context, params *nfsVolumeParams, existingDatasets []tnsapi.Dataset, timer *metrics.OperationTimer) (*tnsapi.Dataset, error) {
	if len(existingDatasets) > 0 {
		dataset := &existingDatasets[0]
		klog.V(4).Infof("Using existing dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)
		return dataset, nil
	}

	// Create new dataset
	dataset, err := s.apiClient.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: params.datasetName,
		Type: "FILESYSTEM",
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create dataset: %v", err)
	}

	klog.V(4).Infof("Created dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)
	return dataset, nil
}

// createNFSShareForDataset creates an NFS share for a dataset.
func (s *ControllerService) createNFSShareForDataset(ctx context.Context, dataset *tnsapi.Dataset, params *nfsVolumeParams, timer *metrics.OperationTimer) (*tnsapi.NFSShare, error) {
	comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", params.volumeName, params.requestedCapacity)
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      comment,
		MaprootUser:  "root",
		MaprootGroup: "wheel",
		Enabled:      true,
	})
	if err != nil {
		klog.Errorf("Failed to create NFS share, cleaning up dataset: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset after NFS share creation failure: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share: %v", err)
	}

	klog.V(4).Infof("Created NFS share with ID: %d for path: %s", nfsShare.ID, nfsShare.Path)
	return nfsShare, nil
}

// createNFSVolume creates an NFS volume with a ZFS dataset and NFS share.
func (s *ControllerService) createNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create")
	klog.V(4).Info("Creating NFS volume")

	// Validate and extract parameters
	params, err := validateNFSParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating dataset: %s with capacity: %d bytes", params.datasetName, params.requestedCapacity)

	// Check if dataset already exists (idempotency)
	existingDatasets, err := s.apiClient.QueryAllDatasets(ctx, params.datasetName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing datasets: %v", err)
	}

	// Handle existing dataset (idempotency check)
	if len(existingDatasets) > 0 {
		resp, done, handleErr := s.handleExistingNFSVolume(ctx, params, &existingDatasets[0], timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// If not done, dataset exists but no NFS share - continue with share creation
	}

	// Create or use existing dataset
	dataset, err := s.getOrCreateDataset(ctx, params, existingDatasets, timer)
	if err != nil {
		return nil, err
	}

	// Create NFS share for the dataset
	nfsShare, err := s.createNFSShareForDataset(ctx, dataset, params, timer)
	if err != nil {
		return nil, err
	}

	// Build and return response
	resp := buildNFSVolumeResponse(params.volumeName, params.server, params.nfsMountOptions, dataset, nfsShare, params.requestedCapacity)

	klog.Infof("Created NFS volume: %s", params.volumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteNFSVolume deletes an NFS volume.
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "delete")
	klog.V(4).Infof("Deleting NFS volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	// Delete ZFS dataset - TrueNAS automatically deletes associated NFS shares
	// when the dataset is deleted, so we don't need to explicitly delete the share
	if meta.DatasetID == "" {
		klog.V(4).Infof("No dataset ID provided, skipping dataset deletion")
	} else {
		klog.V(4).Infof("Deleting dataset: %s (NFS share %d will be automatically removed)", meta.DatasetID, meta.NFSShareID)
		err := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
		if err != nil && !isNotFoundError(err) {
			// For non-idempotent errors, return error to trigger retry and prevent orphaned datasets
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to delete dataset %s: %v", meta.DatasetID, err)
		}
		if err == nil {
			klog.V(4).Infof("Successfully deleted dataset %s and associated NFS share %d", meta.DatasetID, meta.NFSShareID)
		} else {
			klog.V(4).Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
		}
	}

	klog.Infof("Deleted NFS volume: %s", meta.Name)

	// Remove volume capacity metric using plain volume name
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNFS)

	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// setupNFSVolumeFromClone sets up an NFS share for a cloned dataset.
func (s *ControllerService) setupNFSVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.Dataset, server, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up NFS share for cloned dataset: %s", dataset.Name)

	volumeName := req.GetName()
	params := req.GetParameters()

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

	klog.V(4).Infof("Created NFS share with ID: %d for cloned dataset path: %s", nfsShare.ID, nfsShare.Path)

	// Get requested capacity (needed before creating metadata)
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Build volume metadata
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		NFSShareID:  nfsShare.ID,
	}

	// Volume ID is just the volume name (CSI spec compliant)
	volumeID := volumeName

	// Construct volume context with metadata for node plugin
	// CRITICAL: Add clonedFromSnapshot flag to prevent reformatting of cloned volumes
	// ZFS clones inherit filesystems from snapshots, but detection may fail due to caching
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = dataset.Mountpoint
	volumeContext[VolumeContextKeyClonedFromSnap] = VolumeContextValueTrue

	// Pass through custom NFS mount options if specified
	if nfsMountOptions := params[VolumeContextKeyNFSMountOptions]; nfsMountOptions != "" {
		volumeContext[VolumeContextKeyNFSMountOptions] = nfsMountOptions
	}

	klog.Infof("Created NFS volume from snapshot: %s", volumeName)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNFS, requestedCapacity)

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

// expandNFSVolume expands an NFS volume by updating the dataset quota.
//
//nolint:dupl // Similar to expandNVMeOFVolume but with different parameters (Quota vs Volsize, NodeExpansionRequired)
func (s *ControllerService) expandNFSVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "expand")
	klog.V(4).Infof("Expanding NFS volume: %s (dataset: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	// For NFS volumes, we update the quota on the dataset
	// Note: ZFS datasets don't have a strict "size", but we can set a quota
	// to limit the maximum space usage
	klog.V(4).Infof("Expanding NFS dataset - DatasetID: %s, DatasetName: %s, New Quota: %d bytes",
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

	klog.Infof("Expanded NFS volume: %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric using plain volume name
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolNFS, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false, // NFS volumes don't require node-side expansion
	}, nil
}
