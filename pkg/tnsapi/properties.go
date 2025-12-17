// Package tnsapi provides a WebSocket client for TrueNAS Scale API.
package tnsapi

import "strconv"

// ZFS User Property Constants
//
// These properties are stored as ZFS user properties on datasets to track
// CSI metadata. This approach (inspired by democratic-csi) provides:
// - Reliable metadata storage that survives TrueNAS upgrades
// - Ownership verification before deletion (prevents accidental deletion when IDs are reused)
// - Easy debugging via `zfs get all <dataset>` on TrueNAS
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

	// PropertyNFSShareID stores the TrueNAS NFS share ID.
	// Value: e.g., "42" (integer stored as string).
	PropertyNFSShareID = "tns-csi:nfs_share_id"

	// PropertyNVMeSubsystemID stores the TrueNAS NVMe-oF subsystem ID.
	// Value: e.g., "338" (integer stored as string).
	PropertyNVMeSubsystemID = "tns-csi:nvmeof_subsystem_id"

	// PropertyNVMeNamespaceID stores the TrueNAS NVMe-oF namespace ID.
	// Value: e.g., "456" (integer stored as string).
	PropertyNVMeNamespaceID = "tns-csi:nvmeof_namespace_id"

	// PropertyNVMeSubsystemNQN stores the NVMe-oF subsystem NQN for verification.
	// Value: e.g., "nqn.2137.csi.tns:pvc-12345678-1234-1234-1234-123456789012".
	PropertyNVMeSubsystemNQN = "tns-csi:nvmeof_subsystem_nqn"

	// PropertySnapshotSourceVolume stores the source volume for a snapshot.
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertySnapshotSourceVolume = "tns-csi:snapshot_source_volume"

	// PropertySnapshotCSIName stores the CSI snapshot name.
	// Value: e.g., "snapshot-12345678-1234-1234-1234-123456789012".
	PropertySnapshotCSIName = "tns-csi:snapshot_csi_name"

	// PropertyContentSourceType stores the content source type for cloned volumes.
	// Value: "snapshot" or "volume".
	PropertyContentSourceType = "tns-csi:content_source_type"

	// PropertyContentSourceID stores the content source ID for cloned volumes.
	// Value: The snapshot ID or volume ID used as source.
	PropertyContentSourceID = "tns-csi:content_source_id"

	// PropertyProvisionedAt stores the timestamp when the volume was provisioned.
	// Value: RFC3339 timestamp, e.g., "2024-01-15T10:30:00Z".
	PropertyProvisionedAt = "tns-csi:provisioned_at"

	// PropertyProtocol stores the storage protocol used.
	// Value: "nfs" or "nvmeof".
	PropertyProtocol = "tns-csi:protocol"

	// PropertyDeleteStrategy stores the deletion strategy for the volume.
	// Value: "delete" (default) or "retain".
	// When "retain", the volume will not be deleted when the PVC is deleted.
	PropertyDeleteStrategy = "tns-csi:delete_strategy"

	// ManagedByValue is the value stored in PropertyManagedBy.
	ManagedByValue = "tns-csi"

	// ProtocolNFS indicates NFS protocol.
	ProtocolNFS = "nfs"

	// ProtocolNVMeOF indicates NVMe-oF protocol.
	ProtocolNVMeOF = "nvmeof"

	// ContentSourceSnapshot indicates the volume was created from a snapshot.
	ContentSourceSnapshot = "snapshot"

	// ContentSourceVolume indicates the volume was created from another volume (clone).
	ContentSourceVolume = "volume"

	// DeleteStrategyDelete is the default strategy - volume is deleted when PVC is deleted.
	DeleteStrategyDelete = "delete"

	// DeleteStrategyRetain means the volume is retained when PVC is deleted.
	DeleteStrategyRetain = "retain"
)

// PropertyNames returns all tns-csi property names for querying.
func PropertyNames() []string {
	return []string{
		PropertyManagedBy,
		PropertyCSIVolumeName,
		PropertyNFSShareID,
		PropertyNVMeSubsystemID,
		PropertyNVMeNamespaceID,
		PropertyNVMeSubsystemNQN,
		PropertySnapshotSourceVolume,
		PropertySnapshotCSIName,
		PropertyContentSourceType,
		PropertyContentSourceID,
		PropertyProvisionedAt,
		PropertyProtocol,
		PropertyDeleteStrategy,
	}
}

// NFSVolumeProperties returns properties to set when creating an NFS volume.
func NFSVolumeProperties(volumeName string, shareID int, provisionedAt, deleteStrategy string) map[string]string {
	return map[string]string{
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  volumeName,
		PropertyNFSShareID:     intToString(shareID),
		PropertyProtocol:       ProtocolNFS,
		PropertyProvisionedAt:  provisionedAt,
		PropertyDeleteStrategy: deleteStrategy,
	}
}

// NVMeOFVolumeProperties returns properties to set when creating an NVMe-oF volume.
func NVMeOFVolumeProperties(volumeName string, subsystemID, namespaceID int, subsystemNQN, provisionedAt, deleteStrategy string) map[string]string {
	return map[string]string{
		PropertyManagedBy:        ManagedByValue,
		PropertyCSIVolumeName:    volumeName,
		PropertyNVMeSubsystemID:  intToString(subsystemID),
		PropertyNVMeNamespaceID:  intToString(namespaceID),
		PropertyNVMeSubsystemNQN: subsystemNQN,
		PropertyProtocol:         ProtocolNVMeOF,
		PropertyProvisionedAt:    provisionedAt,
		PropertyDeleteStrategy:   deleteStrategy,
	}
}

// ClonedVolumeProperties returns additional properties for cloned volumes.
func ClonedVolumeProperties(sourceType, sourceID string) map[string]string {
	return map[string]string{
		PropertyContentSourceType: sourceType,
		PropertyContentSourceID:   sourceID,
	}
}

// SnapshotProperties returns properties to set on a snapshot's source dataset.
// Note: ZFS snapshots inherit properties from their parent, so we track
// snapshot metadata on the parent dataset or in comments.
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

// StringToInt converts a string to integer, returns 0 on error.
// Exported for use in controllers when reading properties.
func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}
