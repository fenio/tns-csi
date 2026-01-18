// Package tnsapi provides a WebSocket client for TrueNAS Scale API.
package tnsapi

import "strconv"

// Schema versioning for future migrations.
const (
	// PropertySchemaVersion stores the metadata schema version.
	// Value: "1" for Schema v1.
	PropertySchemaVersion = "tns-csi:schema_version"

	// SchemaVersionV1 is the current schema version.
	SchemaVersionV1 = "1"
)

// ZFS User Property Constants - Schema v1
//
// These properties are stored as ZFS user properties on datasets to track
// CSI metadata. This approach (inspired by democratic-csi) provides:
// - Reliable metadata storage that survives TrueNAS upgrades
// - Ownership verification before deletion (prevents accidental deletion when IDs are reused)
// - Easy debugging via `zfs get all <dataset>` on TrueNAS
// - Cross-cluster volume adoption support
//
// All properties use the "tns-csi:" prefix to avoid conflicts with other tools.
const (
	// PropertyPrefix is the prefix for all tns-csi ZFS user properties.
	PropertyPrefix = "tns-csi:"

	// PropertyManagedBy indicates this resource is managed by tns-csi.
	// Value: "tns-csi".
	PropertyManagedBy = "tns-csi:managed_by"

	// PropertyCSIVolumeName stores the CSI volume name (PVC name).
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertyCSIVolumeName = "tns-csi:csi_volume_name"

	// PropertyCapacityBytes stores the volume capacity in bytes.
	// Value: e.g., "10737418240" for 10GiB.
	PropertyCapacityBytes = "tns-csi:capacity_bytes"

	// PropertyProtocol stores the storage protocol used.
	// Value: "nfs", "nvmeof", or "iscsi".
	PropertyProtocol = "tns-csi:protocol"

	// PropertyDeleteStrategy stores the deletion strategy for the volume.
	// Value: "delete" (default) or "retain".
	// When "retain", the volume will not be deleted when the PVC is deleted.
	PropertyDeleteStrategy = "tns-csi:delete_strategy"

	// PropertyCreatedAt stores the timestamp when the volume was created.
	// Value: RFC3339 timestamp, e.g., "2024-01-15T10:30:00Z".
	PropertyCreatedAt = "tns-csi:created_at"
)

// Adoption metadata properties - for cross-cluster volume adoption.
const (
	// PropertyAdoptable marks a volume as adoptable by a new cluster.
	// When set to "true", CreateVolume will automatically adopt this volume
	// if found by CSI volume name, re-creating any missing TrueNAS resources.
	// Value: "true" or "false".
	PropertyAdoptable = "tns-csi:adoptable"

	// PropertyPVCName stores the original PVC name for adoption.
	// Value: e.g., "my-data".
	PropertyPVCName = "tns-csi:pvc_name"

	// PropertyPVCNamespace stores the original PVC namespace for adoption.
	// Value: e.g., "default".
	PropertyPVCNamespace = "tns-csi:pvc_namespace"

	// PropertyStorageClass stores the original StorageClass name for adoption.
	// Value: e.g., "truenas-nfs".
	PropertyStorageClass = "tns-csi:storage_class"
)

// NFS-specific properties.
const (
	// PropertyNFSShareID stores the TrueNAS NFS share ID (mutable on re-share).
	// Value: e.g., "42" (integer stored as string).
	PropertyNFSShareID = "tns-csi:nfs_share_id"

	// PropertyNFSSharePath stores the NFS export path (stable identifier).
	// Value: e.g., "/mnt/tank/csi/pvc-xxx".
	PropertyNFSSharePath = "tns-csi:nfs_share_path"
)

// NVMe-oF-specific properties.
const (
	// PropertyNVMeSubsystemID stores the TrueNAS NVMe-oF subsystem ID (mutable).
	// Value: e.g., "338" (integer stored as string).
	PropertyNVMeSubsystemID = "tns-csi:nvmeof_subsystem_id"

	// PropertyNVMeNamespaceID stores the TrueNAS NVMe-oF namespace ID (mutable).
	// Value: e.g., "456" (integer stored as string).
	PropertyNVMeNamespaceID = "tns-csi:nvmeof_namespace_id"

	// PropertyNVMeSubsystemNQN stores the NVMe-oF subsystem NQN (stable identifier).
	// Value: e.g., "nqn.2024.io.truenas:nvme:pvc-xxx".
	PropertyNVMeSubsystemNQN = "tns-csi:nvmeof_subsystem_nqn"
)

// iSCSI-specific properties (future).
const (
	// PropertyISCSIIQN stores the iSCSI target IQN (stable identifier).
	// Value: e.g., "iqn.2024.io.truenas:target:pvc-xxx".
	PropertyISCSIIQN = "tns-csi:iscsi_iqn"

	// PropertyISCSITargetID stores the TrueNAS iSCSI target ID (mutable).
	// Value: e.g., "10" (integer stored as string).
	PropertyISCSITargetID = "tns-csi:iscsi_target_id"

	// PropertyISCSIExtentID stores the TrueNAS iSCSI extent ID (mutable).
	// Value: e.g., "15" (integer stored as string).
	PropertyISCSIExtentID = "tns-csi:iscsi_extent_id"
)

// Snapshot-specific properties.
const (
	// PropertySnapshotID stores the CSI snapshot ID for detached snapshots.
	// Value: e.g., "snapshot-12345678-1234-1234-1234-123456789012".
	PropertySnapshotID = "tns-csi:snapshot_id"

	// PropertySourceVolumeID stores the source volume ID for snapshots.
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertySourceVolumeID = "tns-csi:source_volume_id"

	// PropertyDetachedSnapshot indicates this dataset is a detached snapshot.
	// Value: "true" or "false".
	PropertyDetachedSnapshot = "tns-csi:detached_snapshot"

	// PropertySourceDataset stores the source dataset path for detached snapshots.
	// Value: e.g., "pool/datasets/pvc-xxx".
	PropertySourceDataset = "tns-csi:source_dataset"

	// PropertySnapshotSourceVolume stores the source volume for a snapshot (legacy).
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertySnapshotSourceVolume = "tns-csi:snapshot_source_volume"

	// PropertySnapshotCSIName stores the CSI snapshot name (legacy).
	// Value: e.g., "snapshot-12345678-1234-1234-1234-123456789012".
	PropertySnapshotCSIName = "tns-csi:snapshot_csi_name"
)

// Clone/content source properties.
const (
	// PropertyContentSourceType stores the content source type for cloned volumes.
	// Value: "snapshot" or "volume".
	PropertyContentSourceType = "tns-csi:content_source_type"

	// PropertyContentSourceID stores the content source ID for cloned volumes.
	// Value: The snapshot ID or volume ID used as source.
	PropertyContentSourceID = "tns-csi:content_source_id"
)

// Legacy property aliases for backward compatibility during migration.
const (
	// PropertyProvisionedAt is an alias for PropertyCreatedAt (legacy name).
	PropertyProvisionedAt = "tns-csi:provisioned_at"
)

// Property values.
const (
	// ManagedByValue is the value stored in PropertyManagedBy.
	ManagedByValue = "tns-csi"

	// ProtocolNFS indicates NFS protocol.
	ProtocolNFS = "nfs"

	// ProtocolNVMeOF indicates NVMe-oF protocol.
	ProtocolNVMeOF = "nvmeof"

	// ProtocolISCSI indicates iSCSI protocol (future).
	ProtocolISCSI = "iscsi"

	// ContentSourceSnapshot indicates the volume was created from a snapshot.
	ContentSourceSnapshot = "snapshot"

	// ContentSourceVolume indicates the volume was created from another volume (clone).
	ContentSourceVolume = "volume"

	// DeleteStrategyDelete is the default strategy - volume is deleted when PVC is deleted.
	DeleteStrategyDelete = "delete"

	// DeleteStrategyRetain means the volume is retained when PVC is deleted.
	DeleteStrategyRetain = "retain"

	// PropertyValueTrue is the string value "true" used in boolean ZFS properties.
	PropertyValueTrue = "true"
)

// PropertyNames returns all tns-csi property names for querying.
func PropertyNames() []string {
	return []string{
		// Schema v1 core properties
		PropertySchemaVersion,
		PropertyManagedBy,
		PropertyCSIVolumeName,
		PropertyCapacityBytes,
		PropertyProtocol,
		PropertyDeleteStrategy,
		PropertyCreatedAt,
		// Adoption properties
		PropertyAdoptable,
		PropertyPVCName,
		PropertyPVCNamespace,
		PropertyStorageClass,
		// NFS properties
		PropertyNFSShareID,
		PropertyNFSSharePath,
		// NVMe-oF properties
		PropertyNVMeSubsystemID,
		PropertyNVMeNamespaceID,
		PropertyNVMeSubsystemNQN,
		// iSCSI properties (future)
		PropertyISCSIIQN,
		PropertyISCSITargetID,
		PropertyISCSIExtentID,
		// Snapshot properties
		PropertySnapshotID,
		PropertySourceVolumeID,
		PropertyDetachedSnapshot,
		PropertySourceDataset,
		PropertySnapshotSourceVolume,
		PropertySnapshotCSIName,
		// Clone properties
		PropertyContentSourceType,
		PropertyContentSourceID,
		// Legacy
		PropertyProvisionedAt,
	}
}

// NFSVolumeParams contains parameters for creating NFS volume properties.
type NFSVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	SharePath      string
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	CapacityBytes  int64
	ShareID        int
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// NFSVolumePropertiesV1 returns Schema v1 properties for an NFS volume.
func NFSVolumePropertiesV1(params NFSVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  params.VolumeID,
		PropertyCapacityBytes:  int64ToString(params.CapacityBytes),
		PropertyProtocol:       ProtocolNFS,
		PropertyCreatedAt:      params.CreatedAt,
		PropertyDeleteStrategy: params.DeleteStrategy,
		PropertyNFSShareID:     intToString(params.ShareID),
		PropertyNFSSharePath:   params.SharePath,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	return props
}

// NFSVolumeProperties returns properties to set when creating an NFS volume.
//
// Deprecated: Use NFSVolumePropertiesV1 for new volumes.
func NFSVolumeProperties(volumeName string, shareID int, provisionedAt, deleteStrategy string) map[string]string {
	return map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  volumeName,
		PropertyNFSShareID:     intToString(shareID),
		PropertyProtocol:       ProtocolNFS,
		PropertyCreatedAt:      provisionedAt,
		PropertyDeleteStrategy: deleteStrategy,
	}
}

// NVMeOFVolumeParams contains parameters for creating NVMe-oF volume properties.
type NVMeOFVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	SubsystemNQN   string
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	CapacityBytes  int64
	SubsystemID    int
	NamespaceID    int
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// NVMeOFVolumePropertiesV1 returns Schema v1 properties for an NVMe-oF volume.
//
//nolint:dupl // Intentionally similar structure to iSCSI volume properties
func NVMeOFVolumePropertiesV1(params NVMeOFVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:    SchemaVersionV1,
		PropertyManagedBy:        ManagedByValue,
		PropertyCSIVolumeName:    params.VolumeID,
		PropertyCapacityBytes:    int64ToString(params.CapacityBytes),
		PropertyProtocol:         ProtocolNVMeOF,
		PropertyCreatedAt:        params.CreatedAt,
		PropertyDeleteStrategy:   params.DeleteStrategy,
		PropertyNVMeSubsystemID:  intToString(params.SubsystemID),
		PropertyNVMeNamespaceID:  intToString(params.NamespaceID),
		PropertyNVMeSubsystemNQN: params.SubsystemNQN,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	return props
}

// NVMeOFVolumeProperties returns properties to set when creating an NVMe-oF volume.
//
// Deprecated: Use NVMeOFVolumePropertiesV1 for new volumes.
func NVMeOFVolumeProperties(volumeName string, subsystemID, namespaceID int, subsystemNQN, provisionedAt, deleteStrategy string) map[string]string {
	return map[string]string{
		PropertySchemaVersion:    SchemaVersionV1,
		PropertyManagedBy:        ManagedByValue,
		PropertyCSIVolumeName:    volumeName,
		PropertyNVMeSubsystemID:  intToString(subsystemID),
		PropertyNVMeNamespaceID:  intToString(namespaceID),
		PropertyNVMeSubsystemNQN: subsystemNQN,
		PropertyProtocol:         ProtocolNVMeOF,
		PropertyCreatedAt:        provisionedAt,
		PropertyDeleteStrategy:   deleteStrategy,
	}
}

// ISCSIVolumeParams contains parameters for creating iSCSI volume properties.
type ISCSIVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	TargetIQN      string
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	CapacityBytes  int64
	TargetID       int
	ExtentID       int
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// ISCSIVolumePropertiesV1 returns Schema v1 properties for an iSCSI volume.
//
//nolint:dupl // Intentionally similar structure to NVMe-oF volume properties
func ISCSIVolumePropertiesV1(params ISCSIVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  params.VolumeID,
		PropertyCapacityBytes:  int64ToString(params.CapacityBytes),
		PropertyProtocol:       ProtocolISCSI,
		PropertyCreatedAt:      params.CreatedAt,
		PropertyDeleteStrategy: params.DeleteStrategy,
		PropertyISCSITargetID:  intToString(params.TargetID),
		PropertyISCSIExtentID:  intToString(params.ExtentID),
		PropertyISCSIIQN:       params.TargetIQN,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	return props
}

// SnapshotParams contains parameters for creating snapshot properties.
type SnapshotParams struct {
	SnapshotID     string
	SourceVolumeID string
	Protocol       string
	SourceDataset  string
	Detached       bool
}

// SnapshotPropertiesV1 returns Schema v1 properties for a snapshot.
func SnapshotPropertiesV1(params SnapshotParams) map[string]string {
	detachedValue := "false"
	if params.Detached {
		detachedValue = "true"
	}
	props := map[string]string{
		PropertySchemaVersion:    SchemaVersionV1,
		PropertyManagedBy:        ManagedByValue,
		PropertySnapshotID:       params.SnapshotID,
		PropertySourceVolumeID:   params.SourceVolumeID,
		PropertyProtocol:         params.Protocol,
		PropertyDetachedSnapshot: detachedValue,
		PropertyDeleteStrategy:   DeleteStrategyDelete,
	}
	if params.SourceDataset != "" {
		props[PropertySourceDataset] = params.SourceDataset
	}
	return props
}

// ClonedVolumeProperties returns additional properties for cloned volumes.
func ClonedVolumeProperties(sourceType, sourceID string) map[string]string {
	return map[string]string{
		PropertyContentSourceType: sourceType,
		PropertyContentSourceID:   sourceID,
	}
}

// SnapshotProperties returns properties to set on a snapshot's source dataset.
//
// Deprecated: Use SnapshotPropertiesV1 for new snapshots.
func SnapshotProperties(snapshotCSIName, sourceVolume string) map[string]string {
	return map[string]string{
		PropertySnapshotCSIName:      snapshotCSIName,
		PropertySnapshotSourceVolume: sourceVolume,
	}
}

// intToString converts an integer to string for ZFS property storage.
func intToString(i int) string {
	return strconv.Itoa(i)
}

// int64ToString converts an int64 to string for ZFS property storage.
func int64ToString(i int64) string {
	return strconv.FormatInt(i, 10)
}

// StringToInt converts a string to integer, returns 0 on error.
// Exported for use in controllers when reading properties.
func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

// StringToInt64 converts a string to int64, returns 0 on error.
// Exported for use in controllers when reading properties.
func StringToInt64(s string) int64 {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return i
}

// GetSchemaVersion extracts the schema version from properties.
// Returns "0" if not set (legacy volume without schema version).
func GetSchemaVersion(props map[string]string) string {
	if v, ok := props[PropertySchemaVersion]; ok && v != "" {
		return v
	}
	return "0"
}

// IsSchemaV1 checks if properties are Schema v1.
func IsSchemaV1(props map[string]string) bool {
	return GetSchemaVersion(props) == SchemaVersionV1
}
