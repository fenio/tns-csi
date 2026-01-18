// Package driver implements iSCSI-specific CSI controller operations.
package driver

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// iscsiVolumeParams holds validated parameters for iSCSI volume creation.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type iscsiVolumeParams struct {
	requestedCapacity int64
	pool              string
	server            string
	parentDataset     string
	volumeName        string
	zvolName          string
	// Generated IQN for this volume's dedicated target
	targetIQN string
	// Portal ID to use for the target (from StorageClass or discovered)
	portalID int
	// Initiator group ID to use for the target
	initiatorID int
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

// generateIQN creates a unique IQN for a volume's dedicated iSCSI target.
// Format: iqn.2024-01.io.truenas.csi:<volume-name>.
func generateIQN(volumeName string) string {
	return "iqn.2024-01.io.truenas.csi:" + volumeName
}

// validateISCSIParams validates and extracts iSCSI volume parameters from the request.
func validateISCSIParams(req *csi.CreateVolumeRequest) (*iscsiVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for iSCSI volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for iSCSI volumes")
	}

	parentDataset := params["parentDataset"]
	if parentDataset == "" {
		parentDataset = pool
	}

	// Extract portal ID if specified (optional - will use first available if not specified)
	var portalID int
	if portalIDStr := params["portalId"]; portalIDStr != "" {
		var err error
		portalID, err = strconv.Atoi(portalIDStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid portalId '%s': %v", portalIDStr, err)
		}
	}

	// Extract initiator ID if specified (optional - will use first available if not specified)
	var initiatorID int
	if initiatorIDStr := params["initiatorId"]; initiatorIDStr != "" {
		var err error
		initiatorID, err = strconv.Atoi(initiatorIDStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid initiatorId '%s': %v", initiatorIDStr, err)
		}
	}

	volumeName := req.GetName()
	zvolName := parentDataset + "/" + volumeName

	// Get delete strategy (default: delete)
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = "delete"
	}

	// Check if volume should be marked as adoptable
	markAdoptable := strings.EqualFold(params["adoptable"], "true")

	// Get capacity
	capacityRange := req.GetCapacityRange()
	var requestedCapacity int64
	if capacityRange != nil {
		requestedCapacity = capacityRange.GetRequiredBytes()
	}
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Parse ZFS ZVOL properties from StorageClass parameters
	zfsProps := parseZFSZvolProperties(params)

	// Parse encryption configuration
	encryptionConf := parseEncryptionConfig(params, req.GetSecrets())

	// Extract adoption metadata from CSI parameters
	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := params["csi.storage.k8s.io/pvc/namespace"]
	storageClass := params["csi.storage.k8s.io/sc/name"]

	return &iscsiVolumeParams{
		requestedCapacity: requestedCapacity,
		pool:              pool,
		server:            server,
		parentDataset:     parentDataset,
		volumeName:        volumeName,
		zvolName:          zvolName,
		targetIQN:         generateIQN(volumeName),
		portalID:          portalID,
		initiatorID:       initiatorID,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		zfsProps:          zfsProps,
		encryption:        encryptionConf,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
	}, nil
}

// buildISCSIVolumeResponse constructs a CSI CreateVolumeResponse for an iSCSI volume.
func buildISCSIVolumeResponse(volumeName, server, targetIQN string, zvol *tnsapi.Dataset, target *tnsapi.ISCSITarget, extent *tnsapi.ISCSIExtent, capacity int64) *csi.CreateVolumeResponse {
	meta := VolumeMetadata{
		Name:          volumeName,
		Protocol:      ProtocolISCSI,
		DatasetID:     zvol.ID,
		DatasetName:   zvol.Name,
		Server:        server,
		ISCSITargetID: target.ID,
		ISCSIExtentID: extent.ID,
		ISCSIIQN:      targetIQN,
	}

	// Volume ID is just the volume name (CSI spec compliant, max 128 bytes)
	volumeID := volumeName

	// Build volume context with all necessary metadata
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(capacity, 10)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolISCSI, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// createISCSIVolume creates an iSCSI volume (ZVOL + extent + target + target-extent).
func (s *ControllerService) createISCSIVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "create")
	klog.V(4).Info("Creating iSCSI volume")

	// Validate and extract parameters
	params, err := validateISCSIParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	// Get iSCSI global config to construct full IQN
	globalConfig, err := s.apiClient.GetISCSIGlobalConfig(ctx)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to get iSCSI global config: %v", err)
	}

	klog.V(4).Infof("Creating iSCSI volume: %s with size: %d bytes, base IQN: %s",
		params.volumeName, params.requestedCapacity, globalConfig.Basename)

	// Check if ZVOL already exists (idempotency)
	existingZvols, err := s.apiClient.QueryAllDatasets(ctx, params.zvolName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing ZVOLs: %v", err)
	}

	// Handle existing ZVOL (idempotency check)
	if len(existingZvols) > 0 {
		resp, done, handleErr := s.handleExistingISCSIVolume(ctx, params, &existingZvols[0], timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// If not done, ZVOL exists but no target/extent - continue with creation
	}

	// Step 1: Create ZVOL
	zvol, err := s.getOrCreateZVOLForISCSI(ctx, params, existingZvols, timer)
	if err != nil {
		return nil, err
	}

	// Step 2: Create iSCSI extent (points to the ZVOL)
	extent, err := s.createISCSIExtent(ctx, params, timer)
	if err != nil {
		// Cleanup: delete ZVOL if extent creation fails
		klog.Errorf("Failed to create iSCSI extent, cleaning up ZVOL: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, err
	}

	// Step 3: Create iSCSI target
	target, err := s.createISCSITarget(ctx, params, timer)
	if err != nil {
		// Cleanup: delete extent and ZVOL
		klog.Errorf("Failed to create iSCSI target, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSIExtent(ctx, extent.ID, false, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI extent: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, err
	}

	// Step 4: Create target-extent association (LUN 0)
	_, err = s.createISCSITargetExtent(ctx, target.ID, extent.ID, timer)
	if err != nil {
		// Cleanup: delete target, extent, and ZVOL
		klog.Errorf("Failed to create target-extent association, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSITarget(ctx, target.ID, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI target: %v", delErr)
		}
		if delErr := s.apiClient.DeleteISCSIExtent(ctx, extent.ID, false, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI extent: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
		}
		return nil, err
	}

	// Step 4.5: Reload iSCSI service to make the new target discoverable
	// Without this, newly created targets may not be visible to iSCSI discovery
	if reloadErr := s.apiClient.ReloadISCSIService(ctx); reloadErr != nil {
		klog.Warningf("Failed to reload iSCSI service (target may not be immediately discoverable): %v", reloadErr)
		// Continue anyway - the target was created, it may just take time to appear
	}

	// Construct full IQN: basename + ":" + target name
	// TrueNAS returns just the target name in target.Name, not the full IQN
	fullIQN := globalConfig.Basename + ":" + target.Name
	klog.V(4).Infof("Constructed full IQN: %s (basename=%s, target=%s)", fullIQN, globalConfig.Basename, target.Name)

	// Step 5: Store ZFS user properties for metadata tracking
	props := tnsapi.ISCSIVolumePropertiesV1(tnsapi.ISCSIVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		TargetID:       target.ID,
		ExtentID:       extent.ID,
		TargetIQN:      fullIQN, // Full IQN for node to use during login
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
	})

	if propErr := s.apiClient.SetDatasetProperties(ctx, zvol.ID, props); propErr != nil {
		klog.Warningf("Failed to set ZFS properties on %s: %v (volume created successfully)", zvol.ID, propErr)
	}

	klog.Infof("Created iSCSI volume: %s (ZVOL: %s, Target: %s, IQN: %s, Extent: %d)",
		params.volumeName, zvol.ID, target.Name, fullIQN, extent.ID)

	timer.ObserveSuccess()
	return buildISCSIVolumeResponse(params.volumeName, params.server, fullIQN, zvol, target, extent, params.requestedCapacity), nil
}

// handleExistingISCSIVolume handles the case when a ZVOL already exists (idempotency).
func (s *ControllerService) handleExistingISCSIVolume(ctx context.Context, params *iscsiVolumeParams, existingZvol *tnsapi.Dataset, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
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

	// Check if target exists for this volume
	target, err := s.apiClient.ISCSITargetByName(ctx, params.volumeName)
	if err != nil {
		// Target doesn't exist - this could mean partial creation, continue to create it
		klog.V(4).Infof("iSCSI target not found for existing ZVOL, will create: %v", err)
		return nil, false, nil
	}

	// Check if extent exists for this ZVOL
	extent, err := s.apiClient.ISCSIExtentByName(ctx, params.volumeName)
	if err != nil {
		klog.V(4).Infof("iSCSI extent not found for existing ZVOL, will create: %v", err)
		return nil, false, nil
	}

	// Get iSCSI global config to construct full IQN
	globalConfig, err := s.apiClient.GetISCSIGlobalConfig(ctx)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to get iSCSI global config: %v", err)
	}

	// Construct full IQN
	fullIQN := globalConfig.Basename + ":" + target.Name

	// Volume already exists with target and extent - return existing volume
	klog.V(4).Infof("iSCSI volume already exists (target ID: %d, extent ID: %d, IQN: %s), returning existing volume",
		target.ID, extent.ID, fullIQN)

	resp := buildISCSIVolumeResponse(params.volumeName, params.server, fullIQN, existingZvol, target, extent, existingCapacity)
	timer.ObserveSuccess()
	return resp, true, nil
}

// getOrCreateZVOLForISCSI creates a ZVOL for iSCSI or returns existing one.
func (s *ControllerService) getOrCreateZVOLForISCSI(ctx context.Context, params *iscsiVolumeParams, existingZvols []tnsapi.Dataset, timer *metrics.OperationTimer) (*tnsapi.Dataset, error) {
	if len(existingZvols) > 0 {
		klog.V(4).Infof("Using existing ZVOL: %s", existingZvols[0].ID)
		return &existingZvols[0], nil
	}

	klog.V(4).Infof("Creating new ZVOL: %s with size %d bytes", params.zvolName, params.requestedCapacity)

	// Build ZVOL create parameters
	createParams := tnsapi.ZvolCreateParams{
		Name:    params.zvolName,
		Volsize: params.requestedCapacity,
		Type:    "VOLUME",
	}

	// Apply ZFS properties if specified in StorageClass
	if params.zfsProps != nil {
		createParams.Compression = params.zfsProps.Compression
		createParams.Dedup = params.zfsProps.Dedup
		createParams.Sync = params.zfsProps.Sync
		createParams.Readonly = params.zfsProps.Readonly
		createParams.Sparse = params.zfsProps.Sparse
		if params.zfsProps.Volblocksize != "" {
			createParams.Volblocksize = params.zfsProps.Volblocksize
		}
	}

	// Apply encryption settings if enabled
	if params.encryption != nil && params.encryption.Enabled {
		createParams.Encryption = true
		// Must disable inherit_encryption when enabling encryption
		inheritEncryption := false
		createParams.InheritEncryption = &inheritEncryption
		if params.encryption.Algorithm != "" {
			createParams.EncryptionOptions = &tnsapi.EncryptionOptions{
				Algorithm: params.encryption.Algorithm,
			}
			// Set passphrase if provided
			if params.encryption.Passphrase != "" {
				createParams.EncryptionOptions.Passphrase = params.encryption.Passphrase
			} else if params.encryption.GenerateKey {
				createParams.EncryptionOptions.GenerateKey = true
			}
		}
	}

	zvol, err := s.apiClient.CreateZvol(ctx, createParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create ZVOL %s: %v", params.zvolName, err)
	}

	klog.V(4).Infof("Created ZVOL: %s (ID: %s)", params.zvolName, zvol.ID)
	return zvol, nil
}

// createISCSIExtent creates an iSCSI extent pointing to the ZVOL.
func (s *ControllerService) createISCSIExtent(ctx context.Context, params *iscsiVolumeParams, timer *metrics.OperationTimer) (*tnsapi.ISCSIExtent, error) {
	klog.V(4).Infof("Creating iSCSI extent for ZVOL: %s", params.zvolName)

	extentParams := tnsapi.ISCSIExtentCreateParams{
		Name: params.volumeName,
		Type: "DISK",
		Disk: "zvol/" + params.zvolName,
	}

	extent, err := s.apiClient.CreateISCSIExtent(ctx, extentParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI extent: %v", err)
	}

	klog.V(4).Infof("Created iSCSI extent: %d for ZVOL %s", extent.ID, params.zvolName)
	return extent, nil
}

// createISCSITarget creates an iSCSI target for the volume.
func (s *ControllerService) createISCSITarget(ctx context.Context, params *iscsiVolumeParams, timer *metrics.OperationTimer) (*tnsapi.ISCSITarget, error) {
	klog.V(4).Infof("Creating iSCSI target for volume: %s", params.volumeName)

	// Get portal and initiator IDs if not specified
	portalID := params.portalID
	initiatorID := params.initiatorID

	if portalID == 0 {
		// Query available portals and use the first one
		portals, err := s.apiClient.QueryISCSIPortals(ctx)
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to query iSCSI portals: %v", err)
		}
		if len(portals) == 0 {
			timer.ObserveError()
			return nil, status.Error(codes.FailedPrecondition, "No iSCSI portals configured on TrueNAS")
		}
		portalID = portals[0].ID
		klog.V(4).Infof("Using first available portal: %d", portalID)
	}

	if initiatorID == 0 {
		// Query available initiators and use the first one
		initiators, err := s.apiClient.QueryISCSIInitiators(ctx)
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to query iSCSI initiators: %v", err)
		}
		if len(initiators) == 0 {
			timer.ObserveError()
			return nil, status.Error(codes.FailedPrecondition, "No iSCSI initiator groups configured on TrueNAS")
		}
		initiatorID = initiators[0].ID
		klog.V(4).Infof("Using first available initiator: %d", initiatorID)
	}

	targetParams := tnsapi.ISCSITargetCreateParams{
		Name: params.volumeName,
		Groups: []tnsapi.ISCSITargetGroup{
			{
				Portal:    portalID,
				Initiator: initiatorID,
			},
		},
	}

	target, err := s.apiClient.CreateISCSITarget(ctx, targetParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target: %v", err)
	}

	klog.V(4).Infof("Created iSCSI target: %s (ID: %d)", target.Name, target.ID)
	return target, nil
}

// createISCSITargetExtent creates a target-extent association (LUN mapping).
func (s *ControllerService) createISCSITargetExtent(ctx context.Context, targetID, extentID int, timer *metrics.OperationTimer) (*tnsapi.ISCSITargetExtent, error) {
	klog.V(4).Infof("Creating target-extent association: target=%d, extent=%d, LUN=0", targetID, extentID)

	teParams := tnsapi.ISCSITargetExtentCreateParams{
		Target: targetID,
		Extent: extentID,
		LunID:  0, // Always use LUN 0 for single-extent targets
	}

	te, err := s.apiClient.CreateISCSITargetExtent(ctx, teParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create target-extent association: %v", err)
	}

	klog.V(4).Infof("Created target-extent association: %d", te.ID)
	return te, nil
}

// deleteISCSIVolume deletes an iSCSI volume and all associated resources.
func (s *ControllerService) deleteISCSIVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "delete")
	klog.Infof("Deleting iSCSI volume: %s (Dataset: %s, Target: %d, Extent: %d)",
		meta.Name, meta.DatasetID, meta.ISCSITargetID, meta.ISCSIExtentID)

	// Check delete strategy from ZFS properties
	props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, []string{tnsapi.PropertyDeleteStrategy})
	if err != nil {
		klog.Warningf("Failed to get delete strategy for %s: %v (will use default 'delete')", meta.DatasetID, err)
	}

	deleteStrategy := props[tnsapi.PropertyDeleteStrategy]
	if deleteStrategy == tnsapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has delete strategy 'retain', skipping deletion", meta.Name)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Step 1: Delete target-extent associations
	if meta.ISCSITargetID != 0 {
		targetExtents, err := s.apiClient.ISCSITargetExtentByTarget(ctx, meta.ISCSITargetID)
		if err != nil {
			klog.Warningf("Failed to query target-extent associations for target %d: %v", meta.ISCSITargetID, err)
		} else {
			for _, te := range targetExtents {
				if delErr := s.apiClient.DeleteISCSITargetExtent(ctx, te.ID, true); delErr != nil {
					klog.Warningf("Failed to delete target-extent %d: %v", te.ID, delErr)
				} else {
					klog.V(4).Infof("Deleted target-extent association: %d", te.ID)
				}
			}
		}
	}

	// Step 2: Delete iSCSI target
	if meta.ISCSITargetID != 0 {
		if err := s.apiClient.DeleteISCSITarget(ctx, meta.ISCSITargetID, true); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete iSCSI target %d: %v", meta.ISCSITargetID, err)
			}
		} else {
			klog.V(4).Infof("Deleted iSCSI target: %d", meta.ISCSITargetID)
		}
	}

	// Step 3: Delete iSCSI extent
	if meta.ISCSIExtentID != 0 {
		if err := s.apiClient.DeleteISCSIExtent(ctx, meta.ISCSIExtentID, false, true); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete iSCSI extent %d: %v", meta.ISCSIExtentID, err)
			}
		} else {
			klog.V(4).Infof("Deleted iSCSI extent: %d", meta.ISCSIExtentID)
		}
	}

	// Step 4: Delete ZVOL
	if meta.DatasetID != "" {
		if err := s.apiClient.DeleteDataset(ctx, meta.DatasetID); err != nil {
			if !isNotFoundError(err) {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to delete ZVOL %s: %v", meta.DatasetID, err)
			}
			klog.V(4).Infof("ZVOL already deleted: %s", meta.DatasetID)
		} else {
			klog.V(4).Infof("Deleted ZVOL: %s", meta.DatasetID)
		}
	}

	// Clear volume capacity metric
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolISCSI)

	klog.Infof("Deleted iSCSI volume: %s", meta.Name)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// expandISCSIVolume expands an iSCSI volume by updating the ZVOL size.
//
//nolint:dupl // Intentionally similar to NFS/NVMe-oF expansion logic
func (s *ControllerService) expandISCSIVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "expand")
	klog.V(4).Infof("Expanding iSCSI volume: %s (ZVOL: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "dataset ID not found in volume metadata")
	}

	// For iSCSI volumes (ZVOLs), we update the volsize property
	klog.V(4).Infof("Expanding iSCSI ZVOL - DatasetID: %s, DatasetName: %s, New Size: %d bytes",
		meta.DatasetID, meta.DatasetName, requiredBytes)

	updateParams := tnsapi.DatasetUpdateParams{
		Volsize: &requiredBytes,
	}

	_, err := s.apiClient.UpdateDataset(ctx, meta.DatasetID, updateParams)
	if err != nil {
		klog.Errorf("Failed to update ZVOL %s (Name: %s): %v", meta.DatasetID, meta.DatasetName, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update ZVOL size for dataset '%s' (Name: '%s'). "+
				"The dataset may not exist on TrueNAS - verify it exists at Storage > Pools. "+
				"Error: %v", meta.DatasetID, meta.DatasetName, err)
	}

	klog.Infof("Expanded iSCSI volume: %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric using plain volume name
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolISCSI, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: true, // iSCSI volumes require node-side filesystem expansion
	}, nil
}

// getISCSIVolumeInfo gets detailed information about an iSCSI volume.
func (s *ControllerService) getISCSIVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting iSCSI volume info for: %s", meta.Name)

	// Get ZVOL dataset info
	dataset, err := s.apiClient.Dataset(ctx, meta.DatasetID)
	if err != nil {
		if isNotFoundError(err) {
			// Volume doesn't exist - return empty response (CSI spec allows this)
			return &csi.ControllerGetVolumeResponse{
				Volume: &csi.Volume{
					VolumeId: meta.Name,
				},
				Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
					VolumeCondition: &csi.VolumeCondition{
						Abnormal: true,
						Message:  "Volume dataset not found on TrueNAS",
					},
				},
			}, nil
		}
		return nil, status.Errorf(codes.Internal, "Failed to get dataset info: %v", err)
	}

	// Get capacity from dataset
	capacity := getZvolCapacity(dataset)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacity,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: false,
				Message:  "Volume is healthy",
			},
		},
	}, nil
}
