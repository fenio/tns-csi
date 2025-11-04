// Package driver implements NFS-specific CSI controller operations.
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

// createNFSVolume creates an NFS volume with a ZFS dataset and NFS share.
func (s *ControllerService) createNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating NFS volume")

	// Get parameters from storage class
	params := req.GetParameters()

	// Required parameters
	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NFS volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NFS volumes")
	}

	// Optional parameters
	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Construct dataset name (parent/volumeID)
	volumeID := req.GetName()
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeID)

	klog.Infof("Creating dataset: %s", datasetName)

	// Step 1: Create ZFS dataset
	dataset, err := s.apiClient.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: datasetName,
		Type: "FILESYSTEM",
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create dataset: %v", err)
	}

	klog.Infof("Created dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)

	// Step 2: Create NFS share for the dataset
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      "CSI Volume: " + volumeID,
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
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share: %v", err)
	}

	klog.Infof("Created NFS share with ID: %d for path: %s", nfsShare.ID, nfsShare.Path)

	// Encode volume metadata into volumeID
	volumeName := req.GetName()
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
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

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	klog.Infof("Successfully created NFS volume with encoded ID: %s", encodedVolumeID)

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
	klog.V(4).Infof("Deleting NFS volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	// Step 1: Delete NFS share
	if meta.NFSShareID > 0 {
		klog.Infof("Deleting NFS share with ID: %d", meta.NFSShareID)
		if err := s.apiClient.DeleteNFSShare(ctx, meta.NFSShareID); err != nil {
			// Log error but continue - the share might already be deleted
			klog.Warningf("Failed to delete NFS share %d: %v (continuing anyway)", meta.NFSShareID, err)
		} else {
			klog.Infof("Successfully deleted NFS share %d", meta.NFSShareID)
		}
	}

	// Step 2: Delete ZFS dataset
	if meta.DatasetID != "" {
		klog.Infof("Deleting dataset: %s", meta.DatasetID)
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			// Check if dataset doesn't exist - this is OK (idempotency)
			klog.Warningf("Failed to delete dataset %s: %v (continuing anyway)", meta.DatasetID, err)
		} else {
			klog.Infof("Successfully deleted dataset %s", meta.DatasetID)
		}
	}

	klog.Infof("Successfully deleted NFS volume: %s", meta.Name)
	return &csi.DeleteVolumeResponse{}, nil
}
