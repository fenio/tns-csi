// Package driver implements iSCSI-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"
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

// iscsiVolumeParams holds validated parameters for iSCSI volume creation.
type iscsiVolumeParams struct {
	zfsProps          *zfsZvolProperties
	encryption        *encryptionConfig
	volumeName        string
	deleteStrategy    string
	storageClass      string
	zvolName          string
	targetIQN         string
	pvcNamespace      string
	pvcName           string
	parentDataset     string
	comment           string
	server            string
	pool              string
	initiatorID       int
	portalID          int
	requestedCapacity int64
	markAdoptable     bool
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

	// Resolve volume name using templating (if configured in StorageClass)
	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}
	zvolName := parentDataset + "/" + volumeName

	// Resolve dataset comment from commentTemplate (if configured in StorageClass)
	comment, err := ResolveComment(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve comment template: %v", err)
	}

	// Get delete strategy (default: delete)
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = verbDelete
	}

	// Check if volume should be marked as adoptable
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

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
		comment:           comment,
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

	// Volume ID is the full dataset path for O(1) lookups (e.g., "pool/parent/pvc-xxx")
	volumeID := zvol.ID

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
	zvol, zvolIsNew, err := s.getOrCreateZVOLForISCSI(ctx, params, existingZvols, timer)
	if err != nil {
		return nil, err
	}

	// Step 2: Create iSCSI extent (points to the ZVOL)
	extent, err := s.createISCSIExtent(ctx, params, timer)
	if err != nil {
		// Cleanup: only delete ZVOL if we just created it (never destroy pre-existing data)
		if zvolIsNew {
			klog.Errorf("Failed to create iSCSI extent, cleaning up newly-created ZVOL: %v", err)
			if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
				klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
			}
		} else {
			klog.Warningf("Failed to create iSCSI extent: %v (skipping ZVOL cleanup — volume was pre-existing)", err)
		}
		return nil, err
	}

	// Step 3: Create iSCSI target
	target, err := s.createISCSITarget(ctx, params, timer)
	if err != nil {
		// Cleanup: delete extent (always new), only delete ZVOL if newly created
		klog.Errorf("Failed to create iSCSI target, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSIExtent(ctx, extent.ID, false, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI extent: %v", delErr)
		}
		if zvolIsNew {
			if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
				klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping ZVOL cleanup — volume was pre-existing")
		}
		return nil, err
	}

	// Step 4: Create target-extent association (LUN 0)
	_, err = s.createISCSITargetExtent(ctx, target.ID, extent.ID, timer)
	if err != nil {
		// Cleanup: delete target and extent (always new), only delete ZVOL if newly created
		klog.Errorf("Failed to create target-extent association, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSITarget(ctx, target.ID, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI target: %v", delErr)
		}
		if delErr := s.apiClient.DeleteISCSIExtent(ctx, extent.ID, false, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI extent: %v", delErr)
		}
		if zvolIsNew {
			if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
				klog.Errorf("Failed to cleanup ZVOL: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping ZVOL cleanup — volume was pre-existing")
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
		ClusterID:      s.clusterID,
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
		// Target lookup by name failed — try property-based fallback (handles name changes across clusters)
		klog.V(4).Infof("iSCSI target not found by name %s, trying property-based fallback", params.volumeName)
		storedProps, propErr := s.apiClient.GetDatasetProperties(ctx, existingZvol.ID, []string{
			tnsapi.PropertyISCSITargetID,
			tnsapi.PropertyISCSIExtentID,
			tnsapi.PropertyISCSIIQN,
		})
		if propErr == nil {
			storedTargetID := tnsapi.StringToInt(storedProps[tnsapi.PropertyISCSITargetID])
			storedExtentID := tnsapi.StringToInt(storedProps[tnsapi.PropertyISCSIExtentID])
			storedIQN := storedProps[tnsapi.PropertyISCSIIQN]
			if storedTargetID > 0 && storedExtentID > 0 && storedIQN != "" {
				klog.Infof("Found stored iSCSI properties: targetID=%d, extentID=%d, IQN=%s — verifying resources exist",
					storedTargetID, storedExtentID, storedIQN)
				// Verify the stored target and extent still exist on TrueNAS
				targets, targetErr := s.apiClient.QueryISCSITargets(ctx, []interface{}{[]interface{}{"id", "=", storedTargetID}})
				extents, extentErr := s.apiClient.QueryISCSIExtents(ctx, []interface{}{[]interface{}{"id", "=", storedExtentID}})
				if targetErr == nil && len(targets) > 0 && extentErr == nil && len(extents) > 0 {
					klog.Infof("iSCSI volume found via stored properties (target=%d, extent=%d, IQN=%s)",
						storedTargetID, storedExtentID, storedIQN)

					s.ensureISCSIProperties(ctx, existingZvol.ID, params, &targets[0], &extents[0], storedIQN)

					resp := buildISCSIVolumeResponse(params.volumeName, params.server, storedIQN, existingZvol, &targets[0], &extents[0], existingCapacity)
					timer.ObserveSuccess()
					return resp, true, nil
				}
			}
		}
		// Property fallback failed too — continue to create
		klog.V(4).Infof("iSCSI target not found for existing ZVOL (including property fallback), will create: %v", err)
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

	// Ensure properties are set (handles retry after context expired during property-setting)
	s.ensureISCSIProperties(ctx, existingZvol.ID, params, target, extent, fullIQN)

	resp := buildISCSIVolumeResponse(params.volumeName, params.server, fullIQN, existingZvol, target, extent, existingCapacity)
	timer.ObserveSuccess()
	return resp, true, nil
}

// ensureISCSIProperties checks if ZFS properties are set on the ZVOL and sets them if missing.
// This handles the case where a ZVOL was created but context expired before properties were set.
func (s *ControllerService) ensureISCSIProperties(ctx context.Context, zvolID string, params *iscsiVolumeParams, target *tnsapi.ISCSITarget, extent *tnsapi.ISCSIExtent, fullIQN string) {
	existing, err := s.apiClient.GetDatasetProperties(ctx, zvolID, []string{tnsapi.PropertyManagedBy})
	if err != nil {
		klog.Warningf("Failed to check properties on ZVOL %s: %v (skipping property recovery)", zvolID, err)
		return
	}
	if existing[tnsapi.PropertyManagedBy] == tnsapi.ManagedByValue {
		return // Properties already set
	}

	klog.Infof("Recovering missing ZFS properties on ZVOL %s (orphaned from interrupted creation)", zvolID)
	props := tnsapi.ISCSIVolumePropertiesV1(tnsapi.ISCSIVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		TargetID:       target.ID,
		ExtentID:       extent.ID,
		TargetIQN:      fullIQN,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      s.clusterID,
	})
	if err := s.apiClient.SetDatasetProperties(ctx, zvolID, props); err != nil {
		klog.Warningf("Failed to recover ZFS properties on ZVOL %s: %v (volume will still work)", zvolID, err)
	} else {
		klog.Infof("Successfully recovered ZFS properties on ZVOL %s", zvolID)
	}
}

// getOrCreateZVOLForISCSI creates a ZVOL for iSCSI or returns existing one.
// Returns (zvol, isNewlyCreated, error). isNewlyCreated is true only when the ZVOL was created
// by this call — callers use this to guard cleanup (never delete pre-existing volumes on failure).
func (s *ControllerService) getOrCreateZVOLForISCSI(ctx context.Context, params *iscsiVolumeParams, existingZvols []tnsapi.Dataset, timer *metrics.OperationTimer) (*tnsapi.Dataset, bool, error) {
	if len(existingZvols) > 0 {
		klog.V(4).Infof("Using existing ZVOL: %s", existingZvols[0].ID)
		return &existingZvols[0], false, nil
	}

	klog.V(4).Infof("Creating new ZVOL: %s with size %d bytes", params.zvolName, params.requestedCapacity)

	// Build ZVOL create parameters
	createParams := tnsapi.ZvolCreateParams{
		Name:     params.zvolName,
		Volsize:  params.requestedCapacity,
		Type:     datasetTypeVolume,
		Comments: params.comment,
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
		return nil, false, createVolumeError(fmt.Sprintf("Failed to create ZVOL %s (%d bytes)", params.zvolName, params.requestedCapacity), err)
	}

	klog.V(4).Infof("Created ZVOL: %s (ID: %s)", params.zvolName, zvol.ID)
	return zvol, true, nil
}

// createISCSIExtent creates an iSCSI extent pointing to the ZVOL.
func (s *ControllerService) createISCSIExtent(ctx context.Context, params *iscsiVolumeParams, timer *metrics.OperationTimer) (*tnsapi.ISCSIExtent, error) {
	klog.V(4).Infof("Creating iSCSI extent for ZVOL: %s", params.zvolName)

	extentParams := tnsapi.ISCSIExtentCreateParams{
		Name: params.volumeName,
		Type: zfsLogicalDiskType,
		Disk: "zvol/" + params.zvolName,
	}

	extent, err := s.apiClient.CreateISCSIExtent(ctx, extentParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI extent for ZVOL %s (target: %s): %v", params.zvolName, params.volumeName, err)
	}

	klog.V(4).Infof("Created iSCSI extent: %d for ZVOL %s", extent.ID, params.zvolName)
	return extent, nil
}

// resolveISCSIPortalAndInitiator resolves portal and initiator IDs, querying TrueNAS if needed.
// Returns the resolved IDs or an error if no portals/initiators are configured.
func (s *ControllerService) resolveISCSIPortalAndInitiator(ctx context.Context, portalID, initiatorID int) (resolvedPortalID, resolvedInitiatorID int, err error) {
	if portalID == 0 {
		portals, err := s.apiClient.QueryISCSIPortals(ctx)
		if err != nil {
			return 0, 0, status.Errorf(codes.Internal, "Failed to query iSCSI portals: %v", err)
		}
		if len(portals) == 0 {
			return 0, 0, status.Error(codes.FailedPrecondition, "No iSCSI portals configured on TrueNAS")
		}
		portalID = portals[0].ID
		klog.V(4).Infof("Using first available portal: %d", portalID)
	}

	if initiatorID == 0 {
		initiators, err := s.apiClient.QueryISCSIInitiators(ctx)
		if err != nil {
			return 0, 0, status.Errorf(codes.Internal, "Failed to query iSCSI initiators: %v", err)
		}
		if len(initiators) == 0 {
			return 0, 0, status.Error(codes.FailedPrecondition, "No iSCSI initiator groups configured on TrueNAS")
		}
		initiatorID = initiators[0].ID
		klog.V(4).Infof("Using first available initiator: %d", initiatorID)
	}

	return portalID, initiatorID, nil
}

// createISCSITarget creates an iSCSI target for the volume.
func (s *ControllerService) createISCSITarget(ctx context.Context, params *iscsiVolumeParams, timer *metrics.OperationTimer) (*tnsapi.ISCSITarget, error) {
	klog.V(4).Infof("Creating iSCSI target for volume: %s", params.volumeName)

	portalID, initiatorID, err := s.resolveISCSIPortalAndInitiator(ctx, params.portalID, params.initiatorID)
	if err != nil {
		timer.ObserveError()
		return nil, err
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
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target '%s' for ZVOL %s: %v", params.volumeName, params.zvolName, err)
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
		return nil, status.Errorf(codes.Internal, "Failed to associate iSCSI target (ID: %d) with extent (ID: %d): %v", targetID, extentID, err)
	}

	klog.V(4).Infof("Created target-extent association: %d", te.ID)
	return te, nil
}

// verifyISCSIOwnership verifies ownership of an iSCSI volume via ZFS properties.
// Returns the deleteStrategy and a "not found" flag. If the ZVOL doesn't exist, returns ("", true, nil)
// so the caller can handle idempotent deletion. Also reconciles stored target/extent IDs with metadata.
func (s *ControllerService) verifyISCSIOwnership(ctx context.Context, meta *VolumeMetadata) (deleteStrategy string, notFound bool, err error) {
	deleteStrategy = tnsapi.DeleteStrategyDelete

	props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, []string{
		tnsapi.PropertyManagedBy,
		tnsapi.PropertyCSIVolumeName,
		tnsapi.PropertyISCSITargetID,
		tnsapi.PropertyISCSIExtentID,
		tnsapi.PropertyDeleteStrategy,
	})
	if err != nil {
		if isNotFoundError(err) {
			return "", true, nil
		}
		klog.Warningf("Failed to verify dataset ownership via ZFS properties: %v (continuing with deletion)", err)
		return deleteStrategy, false, nil
	}

	if managedBy, ok := props[tnsapi.PropertyManagedBy]; ok && managedBy != tnsapi.ManagedByValue {
		return "", false, status.Errorf(codes.FailedPrecondition,
			"Dataset %s is not managed by tns-csi (managed_by=%s)", meta.DatasetID, managedBy)
	}

	if volumeName, ok := props[tnsapi.PropertyCSIVolumeName]; ok {
		nameMatches := volumeName == meta.Name || (isDatasetPathVolumeID(meta.Name) && strings.HasSuffix(meta.Name, "/"+volumeName))
		if !nameMatches {
			return "", false, status.Errorf(codes.FailedPrecondition,
				"Dataset %s volume name mismatch (stored=%s, requested=%s)", meta.DatasetID, volumeName, meta.Name)
		}
	}

	if targetIDStr, ok := props[tnsapi.PropertyISCSITargetID]; ok {
		storedTargetID := tnsapi.StringToInt(targetIDStr)
		if storedTargetID > 0 && meta.ISCSITargetID > 0 && storedTargetID != meta.ISCSITargetID {
			klog.Warningf("iSCSI target ID mismatch: stored=%d, metadata=%d (using stored ID)", storedTargetID, meta.ISCSITargetID)
			meta.ISCSITargetID = storedTargetID
		}
	}

	if extentIDStr, ok := props[tnsapi.PropertyISCSIExtentID]; ok {
		storedExtentID := tnsapi.StringToInt(extentIDStr)
		if storedExtentID > 0 && meta.ISCSIExtentID > 0 && storedExtentID != meta.ISCSIExtentID {
			klog.Warningf("iSCSI extent ID mismatch: stored=%d, metadata=%d (using stored ID)", storedExtentID, meta.ISCSIExtentID)
			meta.ISCSIExtentID = storedExtentID
		}
	}

	if strategy, ok := props[tnsapi.PropertyDeleteStrategy]; ok && strategy != "" {
		deleteStrategy = strategy
	}

	klog.V(4).Infof("Ownership verified for ZVOL %s (volume: %s)", meta.DatasetID, meta.Name)
	return deleteStrategy, false, nil
}

// deleteISCSIVolume deletes an iSCSI volume and all associated resources.
// Uses ZVOL-first delete order: if the ZVOL can't be deleted (dependent clones), bail without
// touching iSCSI resources (target, extent) to prevent orphaning the ZVOL.
//
//nolint:gocognit // Complexity from ownership verification + CSI snapshot guard + dependent clones guard + ZVOL-first delete order
func (s *ControllerService) deleteISCSIVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, verbDelete)
	klog.Infof("Deleting iSCSI volume: %s (Dataset: %s, Target: %d, Extent: %d)",
		meta.Name, meta.DatasetID, meta.ISCSITargetID, meta.ISCSIExtentID)

	// Step 0: Verify ownership via ZFS properties before deletion
	deleteStrategy, notFound, err := s.verifyISCSIOwnership(ctx, meta)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	if notFound {
		klog.V(4).Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	if deleteStrategy == tnsapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has delete strategy 'retain', skipping deletion", meta.Name)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Guard: block deletion if CSI-managed snapshots exist (prevents VolSync deadlock)
	if meta.DatasetID != "" {
		hasCSISnaps, err := s.datasetHasCSIManagedSnapshots(ctx, meta.DatasetID)
		if err != nil {
			// Hard-fail with Unavailable (triggers exponential backoff in CSI sidecars,
			// unlike FailedPrecondition which retries aggressively and floods the WebSocket)
			timer.ObserveError()
			return nil, status.Errorf(codes.Unavailable,
				"cannot verify snapshot state for %s: %v; will retry with backoff", meta.DatasetID, err)
		} else if hasCSISnaps {
			timer.ObserveError()
			return nil, status.Errorf(codes.FailedPrecondition,
				"dataset %s has CSI-managed snapshots; volume will be deleted after snapshots are removed", meta.DatasetID)
		}
	}

	// Step 1: Delete ZVOL first (prevents orphaning iSCSI resources if ZVOL can't be deleted)
	// If the ZVOL has dependent clones, we must bail immediately — deleting target/extent
	// would leave an orphaned ZVOL with no presentation layer, making recovery impossible.
	if meta.DatasetID != "" {
		firstErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
		if firstErr != nil && !isNotFoundError(firstErr) {
			resolved := false
			if isDependentClonesError(firstErr) {
				if err := s.tryPromoteAndDeleteDataset(ctx, meta.DatasetID); err == nil {
					resolved = true
				} else {
					klog.Warningf("ZVOL %s has dependent clones — skipping iSCSI resource cleanup to prevent orphaning", meta.DatasetID)
					timer.ObserveError()
					return nil, status.Errorf(codes.FailedPrecondition,
						"cannot delete volume %s: ZVOL %s has dependent clones; delete the cloned volumes first",
						meta.Name, meta.DatasetID)
				}
			}

			if !resolved {
				// Try snapshot cleanup + retry for other errors
				klog.Infof("Direct deletion failed for %s: %v — cleaning up snapshots before retry",
					meta.DatasetID, firstErr)
				s.deleteDatasetSnapshots(ctx, meta.DatasetID)

				retryConfig := retry.DeletionConfig("delete-iscsi-zvol")
				err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
					deleteErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
					if deleteErr != nil && isNotFoundError(deleteErr) {
						return nil
					}
					return deleteErr
				})

				if err != nil {
					// ZVOL still exists — don't touch iSCSI resources to avoid orphaning
					klog.Errorf("ZVOL %s deletion failed — skipping iSCSI resource cleanup to avoid orphaning: %v", meta.DatasetID, err)
					timer.ObserveError()
					return nil, status.Errorf(codes.Internal,
						"Failed to delete ZVOL %s: %v (iSCSI resources preserved to prevent orphaning)", meta.DatasetID, err)
				}
			}
		}
		klog.V(4).Infof("Deleted ZVOL: %s", meta.DatasetID)
	}

	// Step 2: ZVOL is gone — clean up iSCSI resources (best effort)
	// Even if these fail, data is already deleted and K8s will retry cleanup
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

	if meta.ISCSITargetID != 0 {
		if err := s.apiClient.DeleteISCSITarget(ctx, meta.ISCSITargetID, true); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete iSCSI target %d (ZVOL already deleted, will retry): %v", meta.ISCSITargetID, err)
			}
		} else {
			klog.V(4).Infof("Deleted iSCSI target: %d", meta.ISCSITargetID)
		}
	}

	if meta.ISCSIExtentID != 0 {
		if err := s.apiClient.DeleteISCSIExtent(ctx, meta.ISCSIExtentID, false, true); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete iSCSI extent %d (ZVOL already deleted, will retry): %v", meta.ISCSIExtentID, err)
			}
		} else {
			klog.V(4).Infof("Deleted iSCSI extent: %d", meta.ISCSIExtentID)
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

// getISCSIVolumeInfo retrieves volume information and health status for an iSCSI volume.
func (s *ControllerService) getISCSIVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting iSCSI volume info: %s (dataset: %s, targetID: %d, extentID: %d)",
		meta.Name, meta.DatasetName, meta.ISCSITargetID, meta.ISCSIExtentID)

	abnormal := false
	var messages []string

	// Check 1: Verify ZVOL exists
	var datasets []tnsapi.Dataset
	datasets, err := s.apiClient.QueryAllDatasets(ctx, meta.DatasetName)
	switch {
	case err != nil:
		abnormal = true
		messages = append(messages, fmt.Sprintf("ZVOL %s query failed: %v", meta.DatasetName, err))
	case len(datasets) == 0:
		abnormal = true
		messages = append(messages, fmt.Sprintf("ZVOL %s not found", meta.DatasetName))
	default:
		klog.V(4).Infof("ZVOL %s exists (ID: %s)", meta.DatasetName, datasets[0].ID)
	}

	// Check 2: Verify iSCSI target exists
	if meta.ISCSITargetID > 0 {
		targets, err := s.apiClient.QueryISCSITargets(ctx, []interface{}{
			[]interface{}{"id", "=", meta.ISCSITargetID},
		})
		switch {
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query iSCSI targets: %v", err))
		case len(targets) == 0:
			abnormal = true
			messages = append(messages, fmt.Sprintf("iSCSI target %d not found", meta.ISCSITargetID))
		default:
			klog.V(4).Infof("iSCSI target %d is healthy (name: %s)", targets[0].ID, targets[0].Name)
		}
	}

	// Check 3: Verify iSCSI extent exists and is enabled
	if meta.ISCSIExtentID > 0 {
		extents, err := s.apiClient.QueryISCSIExtents(ctx, []interface{}{
			[]interface{}{"id", "=", meta.ISCSIExtentID},
		})
		switch {
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query iSCSI extents: %v", err))
		case len(extents) == 0:
			abnormal = true
			messages = append(messages, fmt.Sprintf("iSCSI extent %d not found", meta.ISCSIExtentID))
		case !extents[0].Enabled:
			abnormal = true
			messages = append(messages, fmt.Sprintf("iSCSI extent %d is disabled", meta.ISCSIExtentID))
		default:
			klog.V(4).Infof("iSCSI extent %d is healthy (enabled: %t, disk: %s)", extents[0].ID, extents[0].Enabled, extents[0].Disk)
		}
	}

	// Build response message
	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	// Build volume context
	volumeContext := buildVolumeContext(*meta)

	// Get capacity from ZVOL if available
	var capacityBytes int64
	if len(datasets) > 0 {
		capacityBytes = getZvolCapacity(&datasets[0])
	}

	klog.V(4).Infof("iSCSI volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

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

// setupISCSIVolumeFromClone sets up iSCSI infrastructure (extent, target, target-extent) for a cloned ZVOL.
// The ZVOL already exists from the clone operation - this function creates the iSCSI resources on top of it.
func (s *ControllerService) setupISCSIVolumeFromClone(ctx context.Context, req *csi.CreateVolumeRequest, zvol *tnsapi.Dataset, server string, info *cloneInfo) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Setting up iSCSI infrastructure for cloned ZVOL: %s (cloneMode: %s)", zvol.Name, info.Mode)

	volumeName := req.GetName()

	// Get iSCSI global config to construct full IQN
	globalConfig, err := s.apiClient.GetISCSIGlobalConfig(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get iSCSI global config: %v", err)
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Get parameters from request
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	// Extract portal/initiator IDs from params or use first available
	var portalID, initiatorID int
	if portalIDStr := params["portalId"]; portalIDStr != "" {
		portalID, _ = strconv.Atoi(portalIDStr) //nolint:errcheck // Invalid values will use default (0)
	}
	if initiatorIDStr := params["initiatorId"]; initiatorIDStr != "" {
		initiatorID, _ = strconv.Atoi(initiatorIDStr) //nolint:errcheck // Invalid values will use default (0)
	}

	// Resolve portal/initiator IDs (query TrueNAS if not specified)
	portalID, initiatorID, err = s.resolveISCSIPortalAndInitiator(ctx, portalID, initiatorID)
	if err != nil {
		return nil, err
	}

	// Step 1: Create iSCSI extent (points to the cloned ZVOL)
	extent, err := s.apiClient.CreateISCSIExtent(ctx, tnsapi.ISCSIExtentCreateParams{
		Name:      volumeName,
		Type:      zfsLogicalDiskType,
		Disk:      "zvol/" + zvol.ID,
		Blocksize: 512,
	})
	if err != nil {
		// Cleanup: delete the cloned ZVOL if extent creation fails
		klog.Errorf("Failed to create iSCSI extent for cloned ZVOL, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI extent for cloned volume: %v", err)
	}

	klog.V(4).Infof("Created iSCSI extent with ID: %d for cloned ZVOL: %s", extent.ID, zvol.ID)

	// Step 2: Create iSCSI target WITH portal/initiator groups (critical for discoverability!)
	// Without groups, the target won't be advertised on any portal and won't be discoverable.
	target, err := s.apiClient.CreateISCSITarget(ctx, tnsapi.ISCSITargetCreateParams{
		Name: volumeName,
		Mode: iscsiExtentType,
		Groups: []tnsapi.ISCSITargetGroup{
			{
				Portal:    portalID,
				Initiator: initiatorID,
			},
		},
	})
	if err != nil {
		// Cleanup: delete extent and ZVOL
		klog.Errorf("Failed to create iSCSI target, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSIExtent(ctx, extent.ID, false, false); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI extent: %v", delErr)
		}
		if delErr := s.apiClient.DeleteDataset(ctx, zvol.ID); delErr != nil {
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target for cloned volume: %v", err)
	}

	klog.V(4).Infof("Created iSCSI target with ID: %d, Name: %s", target.ID, target.Name)

	// Step 3: Create target-extent association (LUN 0)
	_, err = s.apiClient.CreateISCSITargetExtent(ctx, tnsapi.ISCSITargetExtentCreateParams{
		Target: target.ID,
		Extent: extent.ID,
		LunID:  0,
	})
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
			klog.Errorf("Failed to cleanup cloned ZVOL: %v", delErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to create target-extent association for cloned volume: %v", err)
	}

	// Step 4: Reload iSCSI service to make the new target discoverable
	if reloadErr := s.apiClient.ReloadISCSIService(ctx); reloadErr != nil {
		klog.Warningf("Failed to reload iSCSI service (target may not be immediately discoverable): %v", reloadErr)
	}

	// Construct full IQN
	fullIQN := globalConfig.Basename + ":" + target.Name
	klog.V(4).Infof("Constructed full IQN: %s", fullIQN)

	// Step 5: Store ZFS user properties for metadata tracking
	props := tnsapi.ISCSIVolumePropertiesV1(tnsapi.ISCSIVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		TargetID:       target.ID,
		ExtentID:       extent.ID,
		TargetIQN:      fullIQN,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		ClusterID:      s.clusterID,
	})
	// Add clone-specific properties (including clone mode for dependency tracking)
	cloneProps := tnsapi.ClonedVolumePropertiesV2(tnsapi.ContentSourceSnapshot, info.SnapshotID, info.Mode, info.OriginSnapshot)
	for k, v := range cloneProps {
		props[k] = v
	}
	if err := s.apiClient.SetDatasetProperties(ctx, zvol.ID, props); err != nil {
		klog.Warningf("Failed to set ZFS user properties on cloned ZVOL %s: %v (volume will still work)", zvol.ID, err)
	} else {
		klog.V(4).Infof("Stored ZFS user properties on cloned ZVOL %s", zvol.ID)
	}

	// Set dataset comment from commentTemplate (if configured) — CloneSnapshot doesn't support setting comments
	if comment, commentErr := ResolveComment(req.GetParameters(), req.GetName()); commentErr == nil && comment != "" {
		if _, err := s.apiClient.UpdateDataset(ctx, zvol.ID, tnsapi.DatasetUpdateParams{Comments: comment}); err != nil {
			klog.Warningf("Failed to set comment on cloned ZVOL %s: %v (non-fatal)", zvol.ID, err)
		}
	}

	klog.Infof("Created iSCSI volume from clone: %s (ZVOL: %s, Target: %s, IQN: %s, Extent: %d)",
		volumeName, zvol.ID, target.Name, fullIQN, extent.ID)

	// Build volume metadata
	meta := VolumeMetadata{
		Name:          volumeName,
		Protocol:      ProtocolISCSI,
		DatasetID:     zvol.ID,
		DatasetName:   zvol.Name,
		Server:        server,
		ISCSITargetID: target.ID,
		ISCSIExtentID: extent.ID,
		ISCSIIQN:      fullIQN,
	}

	// Update volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolISCSI, requestedCapacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      zvol.ID,
			CapacityBytes: requestedCapacity,
			VolumeContext: buildVolumeContext(meta),
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

// adoptISCSIVolume adopts an orphaned iSCSI volume by recreating missing TrueNAS resources.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
func (s *ControllerService) adoptISCSIVolume(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.DatasetWithProperties, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting iSCSI volume: %s (dataset=%s)", volumeName, dataset.ID)

	// Get server parameter
	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for iSCSI volumes")
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Get iSCSI global config to construct full IQN
	globalConfig, err := s.apiClient.GetISCSIGlobalConfig(ctx)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to get iSCSI global config: %v", err)
	}

	// Check if target and extent already exist (by looking up stored IDs in properties)
	var target *tnsapi.ISCSITarget
	var extent *tnsapi.ISCSIExtent

	// Try to find existing target by stored IQN
	if iqnProp, ok := dataset.UserProperties[tnsapi.PropertyISCSIIQN]; ok && iqnProp.Value != "" {
		// Extract target name from IQN (format: basename:targetname)
		iqn := iqnProp.Value
		if idx := strings.LastIndex(iqn, ":"); idx != -1 {
			targetName := iqn[idx+1:]
			existingTarget, lookupErr := s.apiClient.ISCSITargetByName(ctx, targetName)
			if lookupErr == nil && existingTarget != nil {
				target = existingTarget
				klog.Infof("Found existing target for adopted volume: ID=%d, Name=%s", target.ID, target.Name)
			}
		}
	}

	// If no target found by IQN, try by volume name
	if target == nil {
		existingTarget, lookupErr := s.apiClient.ISCSITargetByName(ctx, volumeName)
		if lookupErr == nil && existingTarget != nil {
			target = existingTarget
			klog.Infof("Found existing target by volume name: ID=%d, Name=%s", target.ID, target.Name)
		}
	}

	// Try to find existing extent by volume name
	existingExtent, extentErr := s.apiClient.ISCSIExtentByName(ctx, volumeName)
	if extentErr == nil && existingExtent != nil {
		extent = existingExtent
		klog.Infof("Found existing extent for adopted volume: ID=%d, Name=%s", extent.ID, extent.Name)
	}

	// If no target found, create new one
	if target == nil {
		klog.Infof("Creating new iSCSI target for adopted volume: %s", volumeName)

		newTarget, createErr := s.apiClient.CreateISCSITarget(ctx, tnsapi.ISCSITargetCreateParams{
			Name: volumeName,
			Groups: []tnsapi.ISCSITargetGroup{
				{
					Portal:    1, // Default portal
					Initiator: 1, // Default initiator (allow all)
				},
			},
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target for adopted volume: %v", createErr)
		}
		target = newTarget
		klog.Infof("Created iSCSI target for adopted volume: ID=%d, Name=%s", target.ID, target.Name)
	}

	// If no extent found, create one
	if extent == nil {
		klog.Infof("Creating iSCSI extent for adopted volume: %s", volumeName)

		// Extent path for ZVOL
		extentPath := "zvol/" + dataset.Name

		newExtent, createErr := s.apiClient.CreateISCSIExtent(ctx, tnsapi.ISCSIExtentCreateParams{
			Name:      volumeName,
			Type:      zfsLogicalDiskType,
			Disk:      extentPath,
			Blocksize: 512, // Standard block size
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create iSCSI extent for adopted volume: %v", createErr)
		}
		extent = newExtent
		klog.Infof("Created iSCSI extent for adopted volume: ID=%d, Name=%s", extent.ID, extent.Name)
	}

	// Check if target-extent association exists, create if not
	targetExtents, err := s.apiClient.ISCSITargetExtentByTarget(ctx, target.ID)
	if err != nil {
		klog.Warningf("Failed to query target-extent associations: %v", err)
	}

	hasAssociation := false
	for _, te := range targetExtents {
		if te.Extent == extent.ID {
			hasAssociation = true
			klog.Infof("Found existing target-extent association: ID=%d", te.ID)
			break
		}
	}

	if !hasAssociation {
		klog.Infof("Creating target-extent association for adopted volume")
		_, err := s.apiClient.CreateISCSITargetExtent(ctx, tnsapi.ISCSITargetExtentCreateParams{
			Target: target.ID,
			Extent: extent.ID,
			LunID:  0, // LUN 0
		})
		if err != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create target-extent association: %v", err)
		}
		klog.Infof("Created target-extent association for adopted volume")
	}

	// Reload iSCSI service to make the target discoverable
	if reloadErr := s.apiClient.ReloadISCSIService(ctx); reloadErr != nil {
		klog.Warningf("Failed to reload iSCSI service: %v", reloadErr)
	}

	// Construct full IQN
	fullIQN := globalConfig.Basename + ":" + target.Name

	// Update ZFS properties with new IDs
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := tnsapi.ISCSIVolumePropertiesV1(tnsapi.ISCSIVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		TargetID:       target.ID,
		ExtentID:       extent.ID,
		TargetIQN:      fullIQN,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
		ClusterID:      s.clusterID,
	})
	if propErr := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); propErr != nil {
		klog.Warningf("Failed to update ZFS properties on adopted volume %s: %v", dataset.ID, propErr)
	}

	// Build response
	meta := VolumeMetadata{
		Name:          volumeName,
		Protocol:      ProtocolISCSI,
		DatasetID:     dataset.ID,
		DatasetName:   dataset.Name,
		Server:        server,
		ISCSITargetID: target.ID,
		ISCSIExtentID: extent.ID,
		ISCSIIQN:      fullIQN,
	}

	volumeContext := buildVolumeContext(meta)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolISCSI, requestedCapacity)

	klog.Infof("Successfully adopted iSCSI volume: %s (target=%s, IQN=%s)", volumeName, target.Name, fullIQN)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      dataset.ID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}
