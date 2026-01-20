// Package driver implements NFS-specific CSI controller operations.
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

// encryptionConfig holds encryption settings parsed from StorageClass and secrets.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type encryptionConfig struct {
	// Enabled indicates whether encryption should be enabled for the volume.
	Enabled bool
	// Algorithm specifies the encryption algorithm (e.g., AES-256-GCM).
	Algorithm string
	// GenerateKey indicates whether to auto-generate an encryption key.
	GenerateKey bool
	// Passphrase for encryption (from secret).
	Passphrase string
	// Key is a hex-encoded encryption key (from secret).
	Key string
}

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
	// deleteStrategy controls what happens on volume deletion: "delete" (default) or "retain"
	deleteStrategy string
	// markAdoptable marks volumes as adoptable for cross-cluster adoption (StorageClass parameter)
	markAdoptable bool
	// ZFS properties parsed from StorageClass parameters
	zfsProps *zfsDatasetProperties
	// Encryption settings parsed from StorageClass and secrets
	encryption *encryptionConfig
	// Adoption metadata from CSI parameters
	pvcName      string
	pvcNamespace string
	storageClass string
}

// zfsDatasetProperties holds ZFS properties for dataset creation.
// These are parsed from StorageClass parameters with the "zfs." prefix.
type zfsDatasetProperties struct {
	Compression     string
	Dedup           string
	Atime           string
	Sync            string
	Recordsize      string
	Copies          *int
	Snapdir         string
	Readonly        string
	Exec            string
	Aclmode         string
	Acltype         string
	Casesensitivity string
}

// parseZFSDatasetProperties extracts ZFS properties from StorageClass parameters.
// Parameters with the "zfs." prefix are extracted and the prefix is removed.
// Values are normalized to uppercase as required by TrueNAS API.
// Example: "zfs.compression" -> "compression" = "LZ4".
func parseZFSDatasetProperties(params map[string]string) *zfsDatasetProperties {
	props := &zfsDatasetProperties{}
	hasProps := false

	for key, value := range params {
		if !strings.HasPrefix(key, "zfs.") {
			continue
		}
		propName := strings.TrimPrefix(key, "zfs.")
		hasProps = true

		switch propName {
		case "compression":
			// TrueNAS API requires uppercase: ON, OFF, LZ4, GZIP, ZSTD, etc.
			props.Compression = strings.ToUpper(value)
		case "dedup":
			// TrueNAS API requires uppercase: ON, OFF, VERIFY
			props.Dedup = strings.ToUpper(value)
		case "atime":
			// TrueNAS API requires uppercase: ON, OFF, INHERIT
			props.Atime = strings.ToUpper(value)
		case "sync":
			// TrueNAS API requires uppercase: STANDARD, ALWAYS, DISABLED
			props.Sync = strings.ToUpper(value)
		case "recordsize":
			// Recordsize can be like "128K" - normalize to uppercase
			props.Recordsize = strings.ToUpper(value)
		case "copies":
			if copies, err := strconv.Atoi(value); err == nil {
				props.Copies = &copies
			} else {
				klog.Warningf("Invalid zfs.copies value '%s': %v", value, err)
			}
		case "snapdir":
			// TrueNAS API requires uppercase: VISIBLE, HIDDEN
			props.Snapdir = strings.ToUpper(value)
		case "readonly":
			// TrueNAS API requires uppercase: ON, OFF
			props.Readonly = strings.ToUpper(value)
		case "exec":
			// TrueNAS API requires uppercase: ON, OFF
			props.Exec = strings.ToUpper(value)
		case "aclmode":
			// TrueNAS API requires uppercase: PASSTHROUGH, RESTRICTED, etc.
			props.Aclmode = strings.ToUpper(value)
		case "acltype":
			// TrueNAS API requires uppercase: OFF, NFSV4, POSIX
			props.Acltype = strings.ToUpper(value)
		case "casesensitivity":
			// TrueNAS API requires uppercase: SENSITIVE, INSENSITIVE, MIXED
			props.Casesensitivity = strings.ToUpper(value)
		default:
			klog.V(4).Infof("Unknown ZFS property: %s=%s (ignoring)", propName, value)
		}
	}

	if !hasProps {
		return nil
	}

	klog.V(4).Infof("Parsed ZFS dataset properties: compression=%s, dedup=%s, atime=%s, sync=%s, recordsize=%s",
		props.Compression, props.Dedup, props.Atime, props.Sync, props.Recordsize)
	return props
}

// parseEncryptionConfig extracts encryption settings from StorageClass parameters and secrets.
// StorageClass parameters:
//   - encryption: "true" or "false" (default: false)
//   - encryptionAlgorithm: AES-256-GCM (default), AES-128-CCM, AES-192-CCM, AES-256-CCM, AES-128-GCM, AES-192-GCM
//   - encryptionGenerateKey: "true" to auto-generate key (default: false)
//
// Secrets (from csi.storage.k8s.io/provisioner-secret-name):
//   - encryptionPassphrase: passphrase for encryption (min 8 chars)
//   - encryptionKey: hex-encoded 256-bit key (64 chars)
func parseEncryptionConfig(params, secrets map[string]string) *encryptionConfig {
	if !strings.EqualFold(params["encryption"], "true") {
		return nil
	}

	config := &encryptionConfig{
		Enabled:     true,
		Algorithm:   params["encryptionAlgorithm"],
		GenerateKey: strings.EqualFold(params["encryptionGenerateKey"], "true"),
	}

	// Default algorithm if not specified
	if config.Algorithm == "" {
		config.Algorithm = "AES-256-GCM"
	}

	// Get sensitive values from secrets
	if secrets != nil {
		config.Passphrase = secrets["encryptionPassphrase"]
		config.Key = secrets["encryptionKey"]
	}

	// Validate: must have either generateKey, passphrase, or key
	if !config.GenerateKey && config.Passphrase == "" && config.Key == "" {
		klog.Warningf("Encryption enabled but no key source specified. Set encryptionGenerateKey=true, " +
			"or provide encryptionPassphrase/encryptionKey in provisioner secret.")
	}

	klog.V(4).Infof("Parsed encryption config: enabled=%v, algorithm=%s, generateKey=%v, hasPassphrase=%v, hasKey=%v",
		config.Enabled, config.Algorithm, config.GenerateKey, config.Passphrase != "", config.Key != "")

	return config
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
		server = defaultServerAddress // Default for testing
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

	// Resolve volume name using templating (if configured in StorageClass)
	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}
	datasetName := fmt.Sprintf("%s/%s", parentDataset, volumeName)

	// Parse ZFS properties from StorageClass parameters
	zfsProps := parseZFSDatasetProperties(params)

	// Parse encryption config from StorageClass parameters and secrets
	encryption := parseEncryptionConfig(params, req.GetSecrets())

	// Parse deleteStrategy from StorageClass parameters (default: "delete")
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	// Parse markAdoptable from StorageClass parameters (default: false)
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	// Extract adoption metadata from CSI parameters
	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := params["csi.storage.k8s.io/pvc/namespace"]
	storageClass := params["csi.storage.k8s.io/sc/name"]

	return &nfsVolumeParams{
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
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
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
func buildNFSVolumeResponse(volumeName, server string, dataset *tnsapi.Dataset, nfsShare *tnsapi.NFSShare, capacity int64) *csi.CreateVolumeResponse {
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

	resp := buildNFSVolumeResponse(params.volumeName, params.server, existingDataset, &existingShare, capacityToReturn)

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

	// Build dataset creation parameters with ZFS properties
	createParams := tnsapi.DatasetCreateParams{
		Name:     params.datasetName,
		Type:     "FILESYSTEM",
		RefQuota: &params.requestedCapacity, // Set quota at creation for consistency with expansion
	}

	// Apply ZFS properties if specified in StorageClass
	if params.zfsProps != nil {
		createParams.Compression = params.zfsProps.Compression
		createParams.Dedup = params.zfsProps.Dedup
		createParams.Atime = params.zfsProps.Atime
		createParams.Sync = params.zfsProps.Sync
		createParams.Recordsize = params.zfsProps.Recordsize
		createParams.Copies = params.zfsProps.Copies
		createParams.Snapdir = params.zfsProps.Snapdir
		createParams.Readonly = params.zfsProps.Readonly
		createParams.Exec = params.zfsProps.Exec
		createParams.Aclmode = params.zfsProps.Aclmode
		createParams.Acltype = params.zfsProps.Acltype
		createParams.Casesensitivity = params.zfsProps.Casesensitivity

		klog.V(4).Infof("Creating dataset with ZFS properties: compression=%s, dedup=%s, atime=%s",
			createParams.Compression, createParams.Dedup, createParams.Atime)
	}

	// Apply encryption settings if specified in StorageClass
	if params.encryption != nil && params.encryption.Enabled { //nolint:dupl // Intentionally duplicated in NVMe-oF
		createParams.Encryption = true
		// Must disable inherit_encryption when enabling encryption
		inheritEncryption := false
		createParams.InheritEncryption = &inheritEncryption

		// Build encryption options
		encOpts := &tnsapi.EncryptionOptions{
			Algorithm: params.encryption.Algorithm,
		}

		// Determine key source (priority: passphrase > key > generateKey)
		switch {
		case params.encryption.Passphrase != "":
			encOpts.Passphrase = params.encryption.Passphrase
		case params.encryption.Key != "":
			encOpts.Key = params.encryption.Key
		case params.encryption.GenerateKey:
			encOpts.GenerateKey = true
		}

		createParams.EncryptionOptions = encOpts

		klog.V(4).Infof("Creating encrypted dataset with algorithm=%s, generateKey=%v, hasPassphrase=%v, hasKey=%v",
			params.encryption.Algorithm, params.encryption.GenerateKey,
			params.encryption.Passphrase != "", params.encryption.Key != "")
	}

	// Create new dataset
	dataset, err := s.apiClient.CreateDataset(ctx, createParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create dataset: %v", err)
	}

	klog.V(4).Infof("Created dataset: %s with mountpoint: %s", dataset.Name, dataset.Mountpoint)
	return dataset, nil
}

// createNFSShareForDataset creates an NFS share for a dataset and stores ZFS properties for tracking.
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

	// Store ZFS user properties for CSI metadata tracking (Schema v1)
	// This enables safe deletion (verify ownership before delete), debugging, and cross-cluster adoption
	props := tnsapi.NFSVolumePropertiesV1(tnsapi.NFSVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		ShareID:        nfsShare.ID,
		SharePath:      nfsShare.Path,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
	})
	klog.Infof("DEBUG: Storing ZFS properties on dataset %s: deleteStrategy=%q, all props=%v", dataset.ID, params.deleteStrategy, props)
	if err := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); err != nil {
		// Log warning but don't fail - properties are not critical for basic operation
		// Volume will still work, just without the safety features
		klog.Warningf("Failed to set ZFS user properties on dataset %s: %v (volume will still work)", dataset.ID, err)
	} else {
		klog.Infof("DEBUG: Successfully stored ZFS user properties on dataset %s (deleteStrategy=%q)", dataset.ID, params.deleteStrategy)
	}

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
	resp := buildNFSVolumeResponse(params.volumeName, params.server, dataset, nfsShare, params.requestedCapacity)

	klog.Infof("Created NFS volume: %s", params.volumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteNFSVolume deletes an NFS volume with ownership verification.
// Dataset deletion is retried for busy resource errors.
// If deleteStrategy is "retain", the volume is kept but CSI returns success.
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "delete")
	klog.V(4).Infof("Deleting NFS volume: %s (dataset: %s, share ID: %d)", meta.Name, meta.DatasetName, meta.NFSShareID)

	// Step 0: Verify ownership using ZFS properties (safe deletion)
	// This prevents accidental deletion if share IDs were reused after TrueNAS restart
	// Also check deleteStrategy to determine if we should actually delete
	deleteStrategy := tnsapi.DeleteStrategyDelete // Default to delete
	klog.Infof("DEBUG: deleteNFSVolume called for volume %s, datasetID=%q", meta.Name, meta.DatasetID)
	if meta.DatasetID != "" {
		props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, []string{
			tnsapi.PropertyManagedBy,
			tnsapi.PropertyCSIVolumeName,
			tnsapi.PropertyNFSShareID,
			tnsapi.PropertyDeleteStrategy,
		})
		if err != nil {
			// If we can't read properties, the dataset might not exist
			if isNotFoundError(err) {
				klog.V(4).Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
				timer.ObserveSuccess()
				return &csi.DeleteVolumeResponse{}, nil
			}
			// For other errors, log warning but continue (backward compatibility)
			klog.Warningf("Failed to verify dataset ownership via ZFS properties: %v (continuing with deletion)", err)
		} else {
			klog.Infof("DEBUG: Retrieved ZFS properties for dataset %s: %v", meta.DatasetID, props)

			// Verify ownership if properties exist
			if managedBy, ok := props[tnsapi.PropertyManagedBy]; ok && managedBy != tnsapi.ManagedByValue {
				klog.Errorf("Dataset %s is not managed by tns-csi (managed_by=%s), refusing to delete", meta.DatasetID, managedBy)
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Dataset %s is not managed by tns-csi (managed_by=%s)", meta.DatasetID, managedBy)
			}

			// Verify volume name matches
			if volumeName, ok := props[tnsapi.PropertyCSIVolumeName]; ok && volumeName != meta.Name {
				klog.Errorf("Dataset %s volume name mismatch: property=%s, requested=%s", meta.DatasetID, volumeName, meta.Name)
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Dataset %s volume name mismatch (stored=%s, requested=%s)", meta.DatasetID, volumeName, meta.Name)
			}

			// Verify share ID matches (if stored)
			if shareIDStr, ok := props[tnsapi.PropertyNFSShareID]; ok {
				storedShareID := tnsapi.StringToInt(shareIDStr)
				if storedShareID > 0 && meta.NFSShareID > 0 && storedShareID != meta.NFSShareID {
					klog.Warningf("NFS share ID mismatch: stored=%d, metadata=%d (using stored ID)", storedShareID, meta.NFSShareID)
					// Use the stored share ID for deletion as it's more reliable
					meta.NFSShareID = storedShareID
				}
			}

			// Check deleteStrategy
			if strategy, ok := props[tnsapi.PropertyDeleteStrategy]; ok && strategy != "" {
				klog.Infof("DEBUG: Found deleteStrategy property: %q", strategy)
				deleteStrategy = strategy
			} else {
				klog.Infof("DEBUG: No deleteStrategy property found in props, using default: %q", deleteStrategy)
			}

			klog.V(4).Infof("Ownership verified for dataset %s via ZFS properties", meta.DatasetID)
		}
	} else {
		klog.Infof("DEBUG: meta.DatasetID is empty, skipping property retrieval")
	}

	// Check if we should retain the volume instead of deleting
	klog.Infof("DEBUG: Checking deleteStrategy: got %q, comparing with DeleteStrategyRetain=%q, equal=%v",
		deleteStrategy, tnsapi.DeleteStrategyRetain, deleteStrategy == tnsapi.DeleteStrategyRetain)
	if deleteStrategy == tnsapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has deleteStrategy=retain, skipping actual deletion (dataset: %s, share ID: %d will be kept)",
			meta.Name, meta.DatasetID, meta.NFSShareID)
		// Return success per CSI spec - the PV is "deleted" from Kubernetes perspective
		// but the underlying storage is retained
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Step 1: Delete NFS share first (required - TrueNAS does NOT auto-delete shares when dataset is deleted)
	if meta.NFSShareID > 0 {
		klog.V(4).Infof("Deleting NFS share: ID=%d", meta.NFSShareID)
		err := s.apiClient.DeleteNFSShare(ctx, meta.NFSShareID)
		switch {
		case err == nil:
			klog.V(4).Infof("Successfully deleted NFS share %d", meta.NFSShareID)
		case isNotFoundError(err):
			klog.V(4).Infof("NFS share %d not found, assuming already deleted (idempotency)", meta.NFSShareID)
		default:
			// For non-idempotent errors, log warning but continue to try dataset deletion
			// This prevents orphaned datasets if share deletion fails
			klog.Warningf("Failed to delete NFS share %d: %v (continuing with dataset deletion)", meta.NFSShareID, err)
		}
	}

	// Step 2: Delete any snapshots on the dataset first (handles promoted clone cleanup)
	// This is necessary because after ZFS clone promotion, snapshots may have dependent
	// clones that prevent deletion. Deleting with defer=true allows ZFS to clean up properly.
	if meta.DatasetID != "" {
		s.deleteDatasetSnapshots(ctx, meta.DatasetID)
	}

	// Step 3: Delete ZFS dataset with retry logic for busy resources
	if meta.DatasetID == "" {
		klog.V(4).Infof("No dataset ID provided, skipping dataset deletion")
	} else {
		klog.V(4).Infof("Deleting dataset: %s (with retry for busy resources)", meta.DatasetID)

		retryConfig := retry.DeletionConfig("delete-nfs-dataset")
		err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
			deleteErr := s.apiClient.DeleteDataset(ctx, meta.DatasetID)
			if deleteErr != nil && isNotFoundError(deleteErr) {
				// Dataset already deleted - not an error (idempotency)
				klog.V(4).Infof("Dataset %s not found, assuming already deleted (idempotency)", meta.DatasetID)
				return nil
			}
			return deleteErr
		})

		if err != nil {
			// All retries exhausted or non-retryable error
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to delete dataset %s: %v", meta.DatasetID, err)
		}
		klog.V(4).Infof("Successfully deleted dataset %s", meta.DatasetID)
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

	// Get deleteStrategy and adoption metadata from StorageClass parameters
	params := req.GetParameters()
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}

	// Store ZFS user properties for CSI metadata tracking (Schema v1, including clone source info)
	props := tnsapi.NFSVolumePropertiesV1(tnsapi.NFSVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		ShareID:        nfsShare.ID,
		SharePath:      nfsShare.Path,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
	})
	// Add clone-specific properties
	cloneProps := tnsapi.ClonedVolumeProperties(tnsapi.ContentSourceSnapshot, snapshotID)
	for k, v := range cloneProps {
		props[k] = v
	}
	if err := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); err != nil {
		klog.Warningf("Failed to set ZFS user properties on cloned dataset %s: %v (volume will still work)", dataset.ID, err)
	} else {
		klog.V(4).Infof("Stored ZFS user properties on cloned dataset %s: %v", dataset.ID, props)
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

// adoptNFSVolume adopts an orphaned NFS volume by re-creating its NFS share.
// This is called when a volume is found by CSI name but needs to be adopted into a new cluster.
func (s *ControllerService) adoptNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest, dataset *tnsapi.DatasetWithProperties, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting NFS volume: %s (dataset=%s)", volumeName, dataset.ID)

	// Get server parameter
	server := params["server"]
	if server == "" {
		server = defaultServerAddress
	}

	// Get requested capacity (use existing if not expanded)
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Check if dataset has a mountpoint
	if dataset.Mountpoint == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Dataset %s has no mountpoint", dataset.ID)
	}

	// Check if an NFS share already exists for this mountpoint
	existingShares, err := s.apiClient.QueryNFSShare(ctx, dataset.Mountpoint)
	if err != nil {
		klog.Warningf("Failed to query NFS shares for %s: %v", dataset.Mountpoint, err)
	}

	var nfsShare *tnsapi.NFSShare
	if len(existingShares) > 0 {
		// NFS share already exists - use it
		nfsShare = &existingShares[0]
		klog.Infof("Found existing NFS share for adopted volume: ID=%d, path=%s", nfsShare.ID, nfsShare.Path)
	} else {
		// Create new NFS share
		klog.Infof("Creating NFS share for adopted volume: %s", dataset.Mountpoint)
		comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", volumeName, requestedCapacity)
		newShare, createErr := s.apiClient.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
			Path:         dataset.Mountpoint,
			Comment:      comment,
			MaprootUser:  "root",
			MaprootGroup: "wheel",
			Enabled:      true,
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create NFS share for adopted volume: %v", createErr)
		}
		nfsShare = newShare
		klog.Infof("Created NFS share for adopted volume: ID=%d, path=%s", nfsShare.ID, nfsShare.Path)
	}

	// Update ZFS properties with new share ID
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = tnsapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := tnsapi.NFSVolumePropertiesV1(tnsapi.NFSVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		ShareID:        nfsShare.ID,
		SharePath:      nfsShare.Path,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
	})
	if propErr := s.apiClient.SetDatasetProperties(ctx, dataset.ID, props); propErr != nil {
		klog.Warningf("Failed to update ZFS properties on adopted volume %s: %v", dataset.ID, propErr)
	}

	// Build response
	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNFS,
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		Server:      server,
		NFSShareID:  nfsShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = dataset.Mountpoint

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolNFS, requestedCapacity)

	klog.Infof("Successfully adopted NFS volume: %s (shareID=%d)", volumeName, nfsShare.ID)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeName,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
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

	// For NFS volumes, we update the refquota on the dataset
	// Note: ZFS datasets don't have a strict "size", but we can set a refquota
	// to limit the maximum space usage (refquota excludes snapshots)
	klog.V(4).Infof("Expanding NFS dataset - DatasetID: %s, DatasetName: %s, New RefQuota: %d bytes",
		meta.DatasetID, meta.DatasetName, requiredBytes)

	updateParams := tnsapi.DatasetUpdateParams{
		RefQuota: &requiredBytes,
	}

	_, err := s.apiClient.UpdateDataset(ctx, meta.DatasetID, updateParams)
	if err != nil {
		// Provide detailed error information to help diagnose dataset issues
		klog.Errorf("Failed to update dataset refquota for %s (Name: %s): %v", meta.DatasetID, meta.DatasetName, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update dataset refquota for '%s' (Name: '%s'). "+
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
