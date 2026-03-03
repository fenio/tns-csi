// Package driver implements SMB-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
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

// smbVolumeParams holds validated parameters for SMB volume creation.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type smbVolumeParams struct {
	requestedCapacity int64
	pool              string
	server            string
	parentDataset     string
	volumeName        string
	datasetName       string
	deleteStrategy    string
	markAdoptable     bool
	zfsProps          *zfsDatasetProperties
	encryption        *encryptionConfig
	comment           string
	pvcName           string
	pvcNamespace      string
	storageClass      string
}

// validateSMBParams validates and extracts SMB volume parameters from the request.
func validateSMBParams(req *csi.CreateVolumeRequest) (*smbVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for SMB volumes")
	}

	server := params["server"]
	if server == "" {
		server = defaultServerAddress
		klog.V(4).Infof("No server parameter provided, using default: %s", defaultServerAddress)
	}

	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	comment, err := ResolveComment(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve comment template: %v", err)
	}

	zfsProps := parseZFSDatasetProperties(params)
	encryption := parseEncryptionConfig(params, req.GetSecrets())

	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	return &smbVolumeParams{
		pool:              pool,
		server:            server,
		parentDataset:     parentDataset,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		datasetName:       datasetName,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		zfsProps:          zfsProps,
		encryption:        encryption,
		comment:           comment,
		pvcName:           params["csi.storage.k8s.io/pvc/name"],
		pvcNamespace:      params["csi.storage.k8s.io/pvc/namespace"],
		storageClass:      params["csi.storage.k8s.io/sc/name"],
	}, nil
}

// buildSMBVolumeResponse builds the CreateVolumeResponse for an SMB volume.
//
//nolint:dupl // Similar to buildNFSVolumeResponse but uses SMB-specific types
func buildSMBVolumeResponse(volumeName, server string, dataset *tnsapi.Dataset, smbShare *tnsapi.SMBShare, capacity int64) *csi.CreateVolumeResponse {
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolSMB,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		SMBShareID:  smbShare.ID,
	}

	volumeID := dataset.ID

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = smbShare.Name

	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolSMB, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// handleExistingSMBVolume handles the case when a dataset already exists (idempotency).
func (s *ControllerService) handleExistingSMBVolume(ctx context.Context, params *smbVolumeParams, existingDataset *tnsapi.Dataset, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Dataset %s already exists (ID: %s), checking idempotency for SMB", params.datasetName, existingDataset.ID)

	existingShares, err := s.apiClient.QuerySMBShare(ctx, existingDataset.Mountpoint)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to query existing SMB shares: %v", err)
	}

	var existingShare *tnsapi.SMBShare
	for i := range existingShares {
		if existingShares[i].Path == existingDataset.Mountpoint {
			existingShare = &existingShares[i]
			break
		}
	}

	if existingShare == nil {
		return nil, false, nil
	}
	klog.V(4).Infof("SMB volume already exists (share ID: %d), returning existing volume", existingShare.ID)

	resp := buildSMBVolumeResponse(params.volumeName, params.server, existingDataset, existingShare, params.requestedCapacity)

	timer.ObserveSuccess()
	return resp, true, nil
}

// createSMBShareForDataset creates an SMB share for a dataset and stores ZFS properties.
func (s *ControllerService) createSMBShareForDataset(ctx context.Context, dataset *tnsapi.Dataset, params *smbVolumeParams, timer *metrics.OperationTimer) (*tnsapi.SMBShare, error) {
	comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", params.volumeName, params.requestedCapacity)
	smbShare, err := s.apiClient.CreateSMBShare(ctx, tnsapi.SMBShareCreateParams{
		Name:    params.volumeName,
		Path:    dataset.Mountpoint,
		Comment: comment,
		Enabled: true,
	})
	if err != nil {
		klog.Errorf("Failed to create SMB share, cleaning up dataset: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup dataset after SMB share creation failure: %v", delErr)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create SMB share: %v", err)
	}

	klog.V(4).Infof("Created SMB share %q with ID: %d for path: %s", smbShare.Name, smbShare.ID, smbShare.Path)

	props := tnsapi.SMBVolumePropertiesV1(tnsapi.SMBVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		ShareID:        smbShare.ID,
		ShareName:      smbShare.Name,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
	})
	if err := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); err != nil {
		klog.Warningf("Failed to set ZFS user properties on dataset %s: %v (volume will still work)", dataset.ID, err)
	}

	return smbShare, nil
}

// createSMBVolume creates an SMB volume with a ZFS dataset and SMB share.
func (s *ControllerService) createSMBVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "create")
	klog.V(4).Info("Creating SMB volume")

	params, err := validateSMBParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating dataset: %s with capacity: %d bytes", params.datasetName, params.requestedCapacity)

	existingDatasets, err := s.apiClient.QueryAllDatasets(ctx, params.datasetName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing datasets: %v", err)
	}

	if len(existingDatasets) > 0 {
		resp, done, handleErr := s.handleExistingSMBVolume(ctx, params, &existingDatasets[0], timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
	}

	// Reuse NFS's getOrCreateDataset — SMB also uses FILESYSTEM datasets with quota.
	// share_type: "SMB" tells TrueNAS to configure NFSv4 ACLs on the dataset,
	// which is required for SMB sharing (matching democratic-csi's approach).
	nfsParams := &nfsVolumeParams{
		pool:              params.pool,
		server:            params.server,
		parentDataset:     params.parentDataset,
		requestedCapacity: params.requestedCapacity,
		volumeName:        params.volumeName,
		datasetName:       params.datasetName,
		deleteStrategy:    params.deleteStrategy,
		markAdoptable:     params.markAdoptable,
		zfsProps:          params.zfsProps,
		encryption:        params.encryption,
		comment:           params.comment,
		shareType:         "SMB",
	}
	dataset, err := s.getOrCreateDataset(ctx, nfsParams, existingDatasets, timer)
	if err != nil {
		return nil, err
	}

	smbShare, err := s.createSMBShareForDataset(ctx, dataset, params, timer)
	if err != nil {
		return nil, err
	}

	// Set NFSv4 ACLs AFTER share creation — TrueNAS may apply a preset ACL
	// when creating the share, so we override it to allow full access for
	// authenticated SMB users.
	if dataset.Mountpoint != "" {
		if aclErr := s.apiClient.SetFilesystemACL(ctx, dataset.Mountpoint); aclErr != nil {
			klog.Errorf("Failed to set ACL on %s: %v (SMB writes will likely fail with Permission denied)", dataset.Mountpoint, aclErr)
		}
	}

	resp := buildSMBVolumeResponse(params.volumeName, params.server, dataset, smbShare, params.requestedCapacity)

	klog.Infof("Created SMB volume: %s", params.volumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteSMBVolume deletes an SMB volume with ownership verification.
//
//nolint:dupl // Intentionally similar dataset deletion pattern as NFS/iSCSI
func (s *ControllerService) deleteSMBVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "delete")
	klog.V(4).Infof("Deleting SMB volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.SMBShareID)

	deleteStrategy := tnsapi.DeleteStrategyDelete
	if meta.DatasetID != "" {
		props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, []string{
			tnsapi.PropertyManagedBy,
			tnsapi.PropertyCSIVolumeName,
			tnsapi.PropertySMBShareID,
			tnsapi.PropertyDeleteStrategy,
		})
		if err != nil {
			if isNotFoundError(err) {
				klog.V(4).Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
				timer.ObserveSuccess()
				return &csi.DeleteVolumeResponse{}, nil
			}
			klog.Warningf("Failed to verify dataset ownership via ZFS properties: %v (continuing with deletion)", err)
		} else {
			if managedBy, ok := props[tnsapi.PropertyManagedBy]; ok && managedBy != tnsapi.ManagedByValue {
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Dataset %s is not managed by tns-csi (managed_by=%s)", meta.DatasetID, managedBy)
			}

			if volumeName, ok := props[tnsapi.PropertyCSIVolumeName]; ok {
				nameMatches := volumeName == meta.Name || (isDatasetPathVolumeID(meta.Name) && strings.HasSuffix(meta.Name, "/"+volumeName))
				if !nameMatches {
					timer.ObserveError()
					return nil, status.Errorf(codes.FailedPrecondition,
						"Dataset %s volume name mismatch (stored=%s, requested=%s)", meta.DatasetID, volumeName, meta.Name)
				}
			}

			if shareIDStr, ok := props[tnsapi.PropertySMBShareID]; ok {
				storedShareID := tnsapi.StringToInt(shareIDStr)
				if storedShareID > 0 && meta.SMBShareID > 0 && storedShareID != meta.SMBShareID {
					klog.Warningf("SMB share ID mismatch: stored=%d, metadata=%d (using stored ID)", storedShareID, meta.SMBShareID)
					meta.SMBShareID = storedShareID
				}
			}

			if strategy, ok := props[tnsapi.PropertyDeleteStrategy]; ok && strategy != "" {
				deleteStrategy = strategy
			}
		}
	}

	if deleteStrategy == tnsapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has deleteStrategy=retain, skipping actual deletion", meta.Name)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Step 1: Delete SMB share
	if meta.SMBShareID > 0 {
		klog.V(4).Infof("Deleting SMB share: ID=%d", meta.SMBShareID)
		err := s.apiClient.DeleteSMBShare(ctx, meta.SMBShareID)
		switch {
		case err == nil:
			klog.V(4).Infof("Successfully deleted SMB share %d", meta.SMBShareID)
		case isNotFoundError(err):
			klog.V(4).Infof("SMB share %d not found, assuming already deleted (idempotency)", meta.SMBShareID)
		default:
			klog.Warningf("Failed to delete SMB share %d: %v (continuing with dataset deletion)", meta.SMBShareID, err)
		}
	}

	// Step 2: Delete dataset
	if meta.DatasetID != "" {
		klog.V(4).Infof("Deleting dataset: %s", meta.DatasetID)

		firstErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
		if firstErr != nil && !isNotFoundError(firstErr) {
			klog.Infof("Direct deletion failed for %s: %v — cleaning up snapshots before retry", meta.DatasetID, firstErr)
			s.deleteDatasetSnapshots(ctx, meta.DatasetID)

			retryConfig := retry.DeletionConfig("delete-smb-dataset")
			err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
				deleteErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
				if deleteErr != nil && isNotFoundError(deleteErr) {
					return nil
				}
				return deleteErr
			})

			if err != nil {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to delete dataset %s: %v", meta.DatasetID, err)
			}
		}
		klog.V(4).Infof("Successfully deleted dataset %s", meta.DatasetID)
	}

	klog.Infof("Deleted SMB volume: %s", meta.Name)
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolSMB)

	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// setupSMBVolumeFromClone sets up an SMB share for a cloned dataset.
func (s *ControllerService) setupSMBVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.Dataset, server string, info *cloneInfo) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up SMB share for cloned dataset: %s (cloneMode: %s)", dataset.Name, info.Mode)

	volumeName := req.GetName()

	// ZFS clones inherit acltype from the PARENT in the hierarchy (e.g., "storage"),
	// NOT from the origin snapshot's dataset. The parent typically has acltype=posixacl,
	// so clones get POSIX1E ACLs which deny access to SMB users (NT_STATUS_ACCESS_DENIED).
	//
	// Fix: convert the clone's ACLs from POSIX to NFSv4 before creating the SMB share.
	// Step 1: Update dataset properties (acltype, aclmode)
	// Step 2: Set NFSv4 ACEs on the filesystem
	// Step 3: Wait for any async etc.generate('smb') triggered by dataset update to finish
	// Step 4: Create the SMB share (triggers its own etc.generate('smb'))
	// Step 5: Update the share to force a final etc.generate('smb') in case of race
	if dataset.Mountpoint != "" {
		klog.Infof("SMB clone: converting ACLs from POSIX to NFSv4 for %s", dataset.ID)

		_, updateErr := s.apiClient.UpdateDataset(ctx, dataset.ID, tnsapi.DatasetUpdateParams{
			Acltype: "NFSV4",
			Aclmode: "RESTRICTED",
		})
		if updateErr != nil {
			klog.Errorf("SMB clone: failed to update ACL properties on %s: %v", dataset.ID, updateErr)
			if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
				klog.Errorf("Failed to cleanup cloned dataset after ACL update failure: %v", delErr)
			}
			return nil, status.Errorf(codes.Internal, "Failed to set NFSv4 ACL type on cloned dataset: %v", updateErr)
		}
		klog.Infof("SMB clone: updated dataset ACL properties to NFSv4 for %s", dataset.ID)

		if aclErr := s.apiClient.SetFilesystemACL(ctx, dataset.Mountpoint); aclErr != nil {
			klog.Errorf("SMB clone: failed to set NFSv4 ACEs on %s: %v", dataset.Mountpoint, aclErr)
		}

		// Verify the conversion worked.
		if acltype, err := s.apiClient.GetFilesystemACL(ctx, dataset.Mountpoint); err != nil {
			klog.Warningf("SMB clone: failed to verify ACL type for %s: %v", dataset.Mountpoint, err)
		} else {
			klog.Infof("SMB clone: verified ACL type after conversion: acltype=%s for %s", acltype, dataset.Mountpoint)
		}

		// Wait for any async etc.generate('smb') triggered by pool.dataset.update to complete.
		// The dataset update (acltype change) can trigger an async Samba config regeneration
		// that races with the share creation below. If that async regeneration finishes AFTER
		// sharing.smb.create, it overwrites smb4.conf without the new share.
		klog.Infof("SMB clone: waiting 3s for async config generation to settle after ACL conversion")
		time.Sleep(3 * time.Second)
	}

	smbShare, err := s.apiClient.CreateSMBShare(ctx, tnsapi.SMBShareCreateParams{
		Name:    volumeName,
		Path:    dataset.Mountpoint,
		Comment: "CSI Volume (from snapshot): " + volumeName,
		Enabled: true,
	})
	if err != nil {
		klog.Errorf("Failed to create SMB share for cloned dataset, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, dataset.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned dataset after SMB share creation failure: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create SMB share for cloned volume: %v", err)
	}

	// Force a final etc.generate('smb') by updating the share. This guarantees the
	// clone share is in smb4.conf even if the async config generation from the earlier
	// pool.dataset.update overwrote the config produced by sharing.smb.create.
	klog.Infof("SMB clone: forcing config regeneration via share update for %s (shareID: %d)", volumeName, smbShare.ID)
	if _, updateErr := s.apiClient.UpdateSMBShare(ctx, smbShare.ID, tnsapi.SMBShareUpdateParams{
		Comment: smbShare.Comment,
	}); updateErr != nil {
		klog.Warningf("SMB clone: share update for config regeneration failed: %v (mount may still succeed)", updateErr)
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024
	}

	params := req.GetParameters()
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	props := tnsapi.SMBVolumePropertiesV1(tnsapi.SMBVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		ShareID:        smbShare.ID,
		ShareName:      smbShare.Name,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
	})
	cloneProps := tnsapi.ClonedVolumePropertiesV2(tnsapi.ContentSourceSnapshot, info.SnapshotID, info.Mode, info.OriginSnapshot)
	for k, v := range cloneProps {
		props[k] = v
	}
	if err := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); err != nil {
		klog.Warningf("Failed to set ZFS user properties on cloned dataset %s: %v (volume will still work)", dataset.ID, err)
	}

	if comment, commentErr := ResolveComment(req.GetParameters(), req.GetName()); commentErr == nil && comment != "" {
		if _, err := s.apiClient.UpdateDataset(ctx, dataset.ID, tnsapi.DatasetUpdateParams{Comments: comment}); err != nil {
			klog.Warningf("Failed to set comment on cloned dataset %s: %v (non-fatal)", dataset.ID, err)
		}
	}

	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolSMB,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		SMBShareID:  smbShare.ID,
	}

	volumeID := dataset.ID
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = smbShare.Name
	volumeContext[VolumeContextKeyClonedFromSnap] = VolumeContextValueTrue

	klog.Infof("Created SMB volume from snapshot: %s", volumeName)
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolSMB, requestedCapacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
			ContentSource: &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Snapshot{
					Snapshot: &csi.VolumeContentSource_SnapshotSource{
						SnapshotId: info.SnapshotID,
					},
				},
			},
		},
	}, nil
}

// adoptSMBVolume adopts an orphaned SMB volume by re-creating its SMB share.
func (s *ControllerService) adoptSMBVolume(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.DatasetWithProperties, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting SMB volume: %s (dataset=%s)", volumeName, dataset.ID)

	server := params["server"]
	if server == "" {
		server = defaultServerAddress
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024
	}

	if dataset.Mountpoint == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Dataset %s has no mountpoint", dataset.ID)
	}

	existingShares, err := s.apiClient.QuerySMBShare(ctx, dataset.Mountpoint)
	if err != nil {
		klog.Warningf("Failed to query SMB shares for %s: %v", dataset.Mountpoint, err)
	}

	var smbShare *tnsapi.SMBShare
	if len(existingShares) > 0 {
		smbShare = &existingShares[0]
		klog.Infof("Found existing SMB share for adopted volume: ID=%d, name=%s", smbShare.ID, smbShare.Name)
	} else {
		klog.Infof("Creating SMB share for adopted volume: %s", dataset.Mountpoint)
		comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", volumeName, requestedCapacity)
		newShare, createErr := s.apiClient.CreateSMBShare(ctx, tnsapi.SMBShareCreateParams{
			Name:    volumeName,
			Path:    dataset.Mountpoint,
			Comment: comment,
			Enabled: true,
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create SMB share for adopted volume: %v", createErr)
		}
		smbShare = newShare
	}

	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := tnsapi.SMBVolumePropertiesV1(tnsapi.SMBVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		ShareID:        smbShare.ID,
		ShareName:      smbShare.Name,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
	})
	if propErr := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); propErr != nil {
		klog.Warningf("Failed to update ZFS properties on adopted volume %s: %v", dataset.ID, propErr)
	}

	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolSMB,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		SMBShareID:  smbShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = smbShare.Name

	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolSMB, requestedCapacity)

	klog.Infof("Successfully adopted SMB volume: %s (shareID=%d)", volumeName, smbShare.ID)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      dataset.ID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// expandSMBVolume expands an SMB volume by updating the dataset quota.
func (s *ControllerService) expandSMBVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "expand")
	klog.V(4).Infof("Expanding SMB volume: %s (dataset: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	updateParams := tnsapi.DatasetUpdateParams{
		RefQuota: &requiredBytes,
	}

	_, err := s.apiClient.UpdateDataset(ctx, meta.DatasetID, updateParams)
	if err != nil {
		klog.Errorf("Failed to update dataset refquota for %s: %v", meta.DatasetID, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to update dataset refquota for '%s': %v", meta.DatasetID, err)
	}

	klog.Infof("Expanded SMB volume: %s to %d bytes", meta.Name, requiredBytes)
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolSMB, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false, // SMB volumes don't require node-side expansion
	}, nil
}

// getSMBVolumeInfo retrieves volume information and health status for an SMB volume.
func (s *ControllerService) getSMBVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting SMB volume info: %s (dataset: %s, shareID: %d)", meta.Name, meta.DatasetName, meta.SMBShareID)

	abnormal := false
	var messages []string

	dataset, err := s.apiClient.Dataset(ctx, meta.DatasetName)
	if err != nil || dataset == nil {
		abnormal = true
		messages = append(messages, fmt.Sprintf("Dataset %s not accessible: %v", meta.DatasetName, err))
	}

	if meta.SMBShareID > 0 {
		foundShare, err := s.apiClient.QuerySMBShareByID(ctx, meta.SMBShareID)
		if err != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query SMB share %d: %v", meta.SMBShareID, err))
		} else {
			switch {
			case foundShare == nil:
				abnormal = true
				messages = append(messages, fmt.Sprintf("SMB share %d not found", meta.SMBShareID))
			case !foundShare.Enabled:
				abnormal = true
				messages = append(messages, fmt.Sprintf("SMB share %d is disabled", meta.SMBShareID))
			}
		}
	}

	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	volumeContext := buildVolumeContext(*meta)

	var capacityBytes int64
	if dataset != nil && dataset.Available != nil {
		if val, ok := dataset.Available["parsed"].(float64); ok {
			capacityBytes = int64(val)
		}
	}

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: volumeContext,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}
