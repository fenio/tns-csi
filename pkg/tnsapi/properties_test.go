package tnsapi

import (
	"testing"
)

func TestPropertyNames(t *testing.T) {
	names := PropertyNames()

	// Verify we have all expected properties (Schema v1)
	expectedProps := []string{
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

	if len(names) != len(expectedProps) {
		t.Errorf("PropertyNames() returned %d properties, want %d", len(names), len(expectedProps))
	}

	// Check all expected properties are present
	propsMap := make(map[string]bool)
	for _, name := range names {
		propsMap[name] = true
	}

	for _, expected := range expectedProps {
		if !propsMap[expected] {
			t.Errorf("PropertyNames() missing expected property: %s", expected)
		}
	}
}

func TestNFSVolumeProperties(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name           string
		volumeName     string
		shareID        int
		provisionedAt  string
		deleteStrategy string
		wantProps      map[string]string
	}{
		{
			name:           "standard NFS volume",
			volumeName:     "pvc-12345678-1234-1234-1234-123456789012",
			shareID:        42,
			provisionedAt:  "2024-01-15T10:30:00Z",
			deleteStrategy: DeleteStrategyDelete,
			wantProps: map[string]string{
				PropertySchemaVersion:  SchemaVersionV1,
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "pvc-12345678-1234-1234-1234-123456789012",
				PropertyNFSShareID:     "42",
				PropertyProtocol:       ProtocolNFS,
				PropertyCreatedAt:      "2024-01-15T10:30:00Z",
				PropertyDeleteStrategy: DeleteStrategyDelete,
			},
		},
		{
			name:           "NFS volume with retain strategy",
			volumeName:     "my-persistent-volume",
			shareID:        100,
			provisionedAt:  "2025-06-20T14:00:00Z",
			deleteStrategy: DeleteStrategyRetain,
			wantProps: map[string]string{
				PropertySchemaVersion:  SchemaVersionV1,
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "my-persistent-volume",
				PropertyNFSShareID:     "100",
				PropertyProtocol:       ProtocolNFS,
				PropertyCreatedAt:      "2025-06-20T14:00:00Z",
				PropertyDeleteStrategy: DeleteStrategyRetain,
			},
		},
		{
			name:           "NFS volume with zero share ID",
			volumeName:     "test-volume",
			shareID:        0,
			provisionedAt:  "2024-12-01T00:00:00Z",
			deleteStrategy: DeleteStrategyDelete,
			wantProps: map[string]string{
				PropertySchemaVersion:  SchemaVersionV1,
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "test-volume",
				PropertyNFSShareID:     "0",
				PropertyProtocol:       ProtocolNFS,
				PropertyCreatedAt:      "2024-12-01T00:00:00Z",
				PropertyDeleteStrategy: DeleteStrategyDelete,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := NFSVolumeProperties(tt.volumeName, tt.shareID, tt.provisionedAt, tt.deleteStrategy)

			if len(props) != len(tt.wantProps) {
				t.Errorf("NFSVolumeProperties() returned %d properties, want %d", len(props), len(tt.wantProps))
			}

			for key, wantValue := range tt.wantProps {
				if gotValue, ok := props[key]; !ok {
					t.Errorf("NFSVolumeProperties() missing key: %s", key)
				} else if gotValue != wantValue {
					t.Errorf("NFSVolumeProperties()[%s] = %q, want %q", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestNVMeOFVolumeProperties(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name           string
		volumeName     string
		subsystemID    int
		namespaceID    int
		subsystemNQN   string
		provisionedAt  string
		deleteStrategy string
		wantProps      map[string]string
	}{
		{
			name:           "standard NVMe-oF volume",
			volumeName:     "pvc-abcdef00-1234-5678-9abc-def012345678",
			subsystemID:    338,
			namespaceID:    456,
			subsystemNQN:   "nqn.2137.csi.tns:pvc-abcdef00-1234-5678-9abc-def012345678",
			provisionedAt:  "2024-01-15T10:30:00Z",
			deleteStrategy: DeleteStrategyDelete,
			wantProps: map[string]string{
				PropertySchemaVersion:    SchemaVersionV1,
				PropertyManagedBy:        ManagedByValue,
				PropertyCSIVolumeName:    "pvc-abcdef00-1234-5678-9abc-def012345678",
				PropertyNVMeSubsystemID:  "338",
				PropertyNVMeNamespaceID:  "456",
				PropertyNVMeSubsystemNQN: "nqn.2137.csi.tns:pvc-abcdef00-1234-5678-9abc-def012345678",
				PropertyProtocol:         ProtocolNVMeOF,
				PropertyCreatedAt:        "2024-01-15T10:30:00Z",
				PropertyDeleteStrategy:   DeleteStrategyDelete,
			},
		},
		{
			name:           "NVMe-oF volume with retain strategy",
			volumeName:     "database-volume",
			subsystemID:    1,
			namespaceID:    1,
			subsystemNQN:   "nqn.2024.io.example:database",
			provisionedAt:  "2025-12-19T08:00:00Z",
			deleteStrategy: DeleteStrategyRetain,
			wantProps: map[string]string{
				PropertySchemaVersion:    SchemaVersionV1,
				PropertyManagedBy:        ManagedByValue,
				PropertyCSIVolumeName:    "database-volume",
				PropertyNVMeSubsystemID:  "1",
				PropertyNVMeNamespaceID:  "1",
				PropertyNVMeSubsystemNQN: "nqn.2024.io.example:database",
				PropertyProtocol:         ProtocolNVMeOF,
				PropertyCreatedAt:        "2025-12-19T08:00:00Z",
				PropertyDeleteStrategy:   DeleteStrategyRetain,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := NVMeOFVolumeProperties(tt.volumeName, tt.subsystemID, tt.namespaceID, tt.subsystemNQN, tt.provisionedAt, tt.deleteStrategy)

			if len(props) != len(tt.wantProps) {
				t.Errorf("NVMeOFVolumeProperties() returned %d properties, want %d", len(props), len(tt.wantProps))
			}

			for key, wantValue := range tt.wantProps {
				if gotValue, ok := props[key]; !ok {
					t.Errorf("NVMeOFVolumeProperties() missing key: %s", key)
				} else if gotValue != wantValue {
					t.Errorf("NVMeOFVolumeProperties()[%s] = %q, want %q", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestClonedVolumeProperties(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name       string
		sourceType string
		sourceID   string
		wantProps  map[string]string
	}{
		{
			name:       "cloned from snapshot",
			sourceType: ContentSourceSnapshot,
			sourceID:   "snapshot-12345",
			wantProps: map[string]string{
				PropertyContentSourceType: ContentSourceSnapshot,
				PropertyContentSourceID:   "snapshot-12345",
			},
		},
		{
			name:       "cloned from volume",
			sourceType: ContentSourceVolume,
			sourceID:   "pvc-source-volume",
			wantProps: map[string]string{
				PropertyContentSourceType: ContentSourceVolume,
				PropertyContentSourceID:   "pvc-source-volume",
			},
		},
		{
			name:       "empty source type",
			sourceType: "",
			sourceID:   "some-id",
			wantProps: map[string]string{
				PropertyContentSourceType: "",
				PropertyContentSourceID:   "some-id",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := ClonedVolumeProperties(tt.sourceType, tt.sourceID)

			if len(props) != len(tt.wantProps) {
				t.Errorf("ClonedVolumeProperties() returned %d properties, want %d", len(props), len(tt.wantProps))
			}

			for key, wantValue := range tt.wantProps {
				if gotValue, ok := props[key]; !ok {
					t.Errorf("ClonedVolumeProperties() missing key: %s", key)
				} else if gotValue != wantValue {
					t.Errorf("ClonedVolumeProperties()[%s] = %q, want %q", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestSnapshotProperties(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name            string
		snapshotCSIName string
		sourceVolume    string
		wantProps       map[string]string
	}{
		{
			name:            "standard snapshot",
			snapshotCSIName: "snapshot-abcd1234",
			sourceVolume:    "pvc-12345678",
			wantProps: map[string]string{
				PropertySnapshotCSIName:      "snapshot-abcd1234",
				PropertySnapshotSourceVolume: "pvc-12345678",
			},
		},
		{
			name:            "snapshot with long names",
			snapshotCSIName: "snapshot-12345678-1234-1234-1234-123456789012",
			sourceVolume:    "pvc-abcdef00-1234-5678-9abc-def012345678",
			wantProps: map[string]string{
				PropertySnapshotCSIName:      "snapshot-12345678-1234-1234-1234-123456789012",
				PropertySnapshotSourceVolume: "pvc-abcdef00-1234-5678-9abc-def012345678",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := SnapshotProperties(tt.snapshotCSIName, tt.sourceVolume)

			if len(props) != len(tt.wantProps) {
				t.Errorf("SnapshotProperties() returned %d properties, want %d", len(props), len(tt.wantProps))
			}

			for key, wantValue := range tt.wantProps {
				if gotValue, ok := props[key]; !ok {
					t.Errorf("SnapshotProperties() missing key: %s", key)
				} else if gotValue != wantValue {
					t.Errorf("SnapshotProperties()[%s] = %q, want %q", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestStringToInt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "positive integer",
			input: "42",
			want:  42,
		},
		{
			name:  "zero",
			input: "0",
			want:  0,
		},
		{
			name:  "negative integer",
			input: "-10",
			want:  -10,
		},
		{
			name:  "large number",
			input: "999999999",
			want:  999999999,
		},
		{
			name:  "empty string returns 0",
			input: "",
			want:  0,
		},
		{
			name:  "non-numeric string returns 0",
			input: "not-a-number",
			want:  0,
		},
		{
			name:  "float string returns 0",
			input: "3.14",
			want:  0,
		},
		{
			name:  "whitespace returns 0",
			input: "  ",
			want:  0,
		},
		{
			name:  "number with spaces returns 0",
			input: " 42 ",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StringToInt(tt.input)
			if got != tt.want {
				t.Errorf("StringToInt(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestIntToString(t *testing.T) {
	// intToString is unexported, but we can test it indirectly via NFSVolumeProperties
	// which uses it for shareID conversion
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name    string
		shareID int
		want    string
	}{
		{
			name:    "positive integer",
			shareID: 42,
			want:    "42",
		},
		{
			name:    "zero",
			shareID: 0,
			want:    "0",
		},
		{
			name:    "large number",
			shareID: 999999999,
			want:    "999999999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := NFSVolumeProperties("test", tt.shareID, "2024-01-01T00:00:00Z", DeleteStrategyDelete)
			got := props[PropertyNFSShareID]
			if got != tt.want {
				t.Errorf("intToString(%d) via NFSVolumeProperties = %q, want %q", tt.shareID, got, tt.want)
			}
		})
	}
}

func TestPropertyConstants(t *testing.T) {
	// Verify all property constants have the correct prefix
	props := []string{
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
		// iSCSI properties
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

	for _, prop := range props {
		if len(prop) < len(PropertyPrefix) || prop[:len(PropertyPrefix)] != PropertyPrefix {
			t.Errorf("Property %q does not have prefix %q", prop, PropertyPrefix)
		}
	}
}

func TestValueConstants(t *testing.T) {
	// Verify value constants are what we expect
	if ManagedByValue != "tns-csi" {
		t.Errorf("ManagedByValue = %q, want %q", ManagedByValue, "tns-csi")
	}

	if ProtocolNFS != "nfs" {
		t.Errorf("ProtocolNFS = %q, want %q", ProtocolNFS, "nfs")
	}

	if ProtocolNVMeOF != "nvmeof" {
		t.Errorf("ProtocolNVMeOF = %q, want %q", ProtocolNVMeOF, "nvmeof")
	}

	if ContentSourceSnapshot != "snapshot" {
		t.Errorf("ContentSourceSnapshot = %q, want %q", ContentSourceSnapshot, "snapshot")
	}

	if ContentSourceVolume != "volume" {
		t.Errorf("ContentSourceVolume = %q, want %q", ContentSourceVolume, "volume")
	}

	if DeleteStrategyDelete != "delete" {
		t.Errorf("DeleteStrategyDelete = %q, want %q", DeleteStrategyDelete, "delete")
	}

	if DeleteStrategyRetain != "retain" {
		t.Errorf("DeleteStrategyRetain = %q, want %q", DeleteStrategyRetain, "retain")
	}

	if ProtocolISCSI != "iscsi" {
		t.Errorf("ProtocolISCSI = %q, want %q", ProtocolISCSI, "iscsi")
	}

	if SchemaVersionV1 != "1" {
		t.Errorf("SchemaVersionV1 = %q, want %q", SchemaVersionV1, "1")
	}
}

func TestNFSVolumePropertiesV1(t *testing.T) {
	params := NFSVolumeParams{
		VolumeID:       "pvc-12345678-1234-1234-1234-123456789012",
		CreatedAt:      "2024-01-15T10:30:00Z",
		DeleteStrategy: DeleteStrategyDelete,
		SharePath:      "/mnt/tank/csi/pvc-12345678",
		PVCName:        "my-data",
		PVCNamespace:   "default",
		StorageClass:   "truenas-nfs",
		CapacityBytes:  10737418240,
		ShareID:        42,
	}

	props := NFSVolumePropertiesV1(params)

	// Check core properties
	if props[PropertySchemaVersion] != SchemaVersionV1 {
		t.Errorf("PropertySchemaVersion = %q, want %q", props[PropertySchemaVersion], SchemaVersionV1)
	}
	if props[PropertyManagedBy] != ManagedByValue {
		t.Errorf("PropertyManagedBy = %q, want %q", props[PropertyManagedBy], ManagedByValue)
	}
	if props[PropertyCSIVolumeName] != params.VolumeID {
		t.Errorf("PropertyCSIVolumeName = %q, want %q", props[PropertyCSIVolumeName], params.VolumeID)
	}
	if props[PropertyProtocol] != ProtocolNFS {
		t.Errorf("PropertyProtocol = %q, want %q", props[PropertyProtocol], ProtocolNFS)
	}
	if props[PropertyCapacityBytes] != "10737418240" {
		t.Errorf("PropertyCapacityBytes = %q, want %q", props[PropertyCapacityBytes], "10737418240")
	}
	if props[PropertyCreatedAt] != params.CreatedAt {
		t.Errorf("PropertyCreatedAt = %q, want %q", props[PropertyCreatedAt], params.CreatedAt)
	}
	if props[PropertyDeleteStrategy] != params.DeleteStrategy {
		t.Errorf("PropertyDeleteStrategy = %q, want %q", props[PropertyDeleteStrategy], params.DeleteStrategy)
	}

	// Check NFS-specific properties
	if props[PropertyNFSShareID] != "42" {
		t.Errorf("PropertyNFSShareID = %q, want %q", props[PropertyNFSShareID], "42")
	}
	if props[PropertyNFSSharePath] != params.SharePath {
		t.Errorf("PropertyNFSSharePath = %q, want %q", props[PropertyNFSSharePath], params.SharePath)
	}

	// Check adoption properties
	if props[PropertyPVCName] != params.PVCName {
		t.Errorf("PropertyPVCName = %q, want %q", props[PropertyPVCName], params.PVCName)
	}
	if props[PropertyPVCNamespace] != params.PVCNamespace {
		t.Errorf("PropertyPVCNamespace = %q, want %q", props[PropertyPVCNamespace], params.PVCNamespace)
	}
	if props[PropertyStorageClass] != params.StorageClass {
		t.Errorf("PropertyStorageClass = %q, want %q", props[PropertyStorageClass], params.StorageClass)
	}
}

func TestNFSVolumePropertiesV1_OptionalAdoption(t *testing.T) {
	// Test that adoption properties are omitted when empty
	params := NFSVolumeParams{
		VolumeID:       "pvc-test",
		CreatedAt:      "2024-01-15T10:30:00Z",
		DeleteStrategy: DeleteStrategyDelete,
		SharePath:      "/mnt/tank/csi/pvc-test",
		CapacityBytes:  1073741824,
		ShareID:        1,
		// Adoption fields left empty
	}

	props := NFSVolumePropertiesV1(params)

	if _, ok := props[PropertyPVCName]; ok {
		t.Error("PropertyPVCName should not be set when empty")
	}
	if _, ok := props[PropertyPVCNamespace]; ok {
		t.Error("PropertyPVCNamespace should not be set when empty")
	}
	if _, ok := props[PropertyStorageClass]; ok {
		t.Error("PropertyStorageClass should not be set when empty")
	}
}

func TestNVMeOFVolumePropertiesV1(t *testing.T) {
	params := NVMeOFVolumeParams{
		VolumeID:       "pvc-abcdef00-1234-5678-9abc-def012345678",
		CreatedAt:      "2024-01-15T10:30:00Z",
		DeleteStrategy: DeleteStrategyRetain,
		SubsystemNQN:   "nqn.2024.io.truenas:nvme:pvc-abcdef00",
		PVCName:        "database",
		PVCNamespace:   "production",
		StorageClass:   "truenas-nvmeof",
		CapacityBytes:  53687091200,
		SubsystemID:    338,
		NamespaceID:    456,
	}

	props := NVMeOFVolumePropertiesV1(params)

	// Check core properties
	if props[PropertySchemaVersion] != SchemaVersionV1 {
		t.Errorf("PropertySchemaVersion = %q, want %q", props[PropertySchemaVersion], SchemaVersionV1)
	}
	if props[PropertyProtocol] != ProtocolNVMeOF {
		t.Errorf("PropertyProtocol = %q, want %q", props[PropertyProtocol], ProtocolNVMeOF)
	}
	if props[PropertyCapacityBytes] != "53687091200" {
		t.Errorf("PropertyCapacityBytes = %q, want %q", props[PropertyCapacityBytes], "53687091200")
	}

	// Check NVMe-oF-specific properties
	if props[PropertyNVMeSubsystemID] != "338" {
		t.Errorf("PropertyNVMeSubsystemID = %q, want %q", props[PropertyNVMeSubsystemID], "338")
	}
	if props[PropertyNVMeNamespaceID] != "456" {
		t.Errorf("PropertyNVMeNamespaceID = %q, want %q", props[PropertyNVMeNamespaceID], "456")
	}
	if props[PropertyNVMeSubsystemNQN] != params.SubsystemNQN {
		t.Errorf("PropertyNVMeSubsystemNQN = %q, want %q", props[PropertyNVMeSubsystemNQN], params.SubsystemNQN)
	}

	// Check adoption properties
	if props[PropertyPVCName] != params.PVCName {
		t.Errorf("PropertyPVCName = %q, want %q", props[PropertyPVCName], params.PVCName)
	}
}

func TestSnapshotPropertiesV1(t *testing.T) {
	params := SnapshotParams{
		SnapshotID:     "snapshot-12345678-1234-1234-1234-123456789012",
		SourceVolumeID: "pvc-source-volume",
		Protocol:       ProtocolNFS,
		SourceDataset:  "pool/datasets/pvc-source",
		Detached:       false,
	}

	props := SnapshotPropertiesV1(params)

	if props[PropertySchemaVersion] != SchemaVersionV1 {
		t.Errorf("PropertySchemaVersion = %q, want %q", props[PropertySchemaVersion], SchemaVersionV1)
	}
	if props[PropertyManagedBy] != ManagedByValue {
		t.Errorf("PropertyManagedBy = %q, want %q", props[PropertyManagedBy], ManagedByValue)
	}
	if props[PropertySnapshotID] != params.SnapshotID {
		t.Errorf("PropertySnapshotID = %q, want %q", props[PropertySnapshotID], params.SnapshotID)
	}
	if props[PropertySourceVolumeID] != params.SourceVolumeID {
		t.Errorf("PropertySourceVolumeID = %q, want %q", props[PropertySourceVolumeID], params.SourceVolumeID)
	}
	if props[PropertyProtocol] != params.Protocol {
		t.Errorf("PropertyProtocol = %q, want %q", props[PropertyProtocol], params.Protocol)
	}
	if props[PropertyDetachedSnapshot] != "false" {
		t.Errorf("PropertyDetachedSnapshot = %q, want %q", props[PropertyDetachedSnapshot], "false")
	}
	if props[PropertySourceDataset] != params.SourceDataset {
		t.Errorf("PropertySourceDataset = %q, want %q", props[PropertySourceDataset], params.SourceDataset)
	}
	if props[PropertyDeleteStrategy] != DeleteStrategyDelete {
		t.Errorf("PropertyDeleteStrategy = %q, want %q", props[PropertyDeleteStrategy], DeleteStrategyDelete)
	}
}

func TestSnapshotPropertiesV1_Detached(t *testing.T) {
	params := SnapshotParams{
		SnapshotID:     "snapshot-detached",
		SourceVolumeID: "pvc-source",
		Protocol:       ProtocolNVMeOF,
		Detached:       true,
	}

	props := SnapshotPropertiesV1(params)

	if props[PropertyDetachedSnapshot] != "true" {
		t.Errorf("PropertyDetachedSnapshot = %q, want %q", props[PropertyDetachedSnapshot], "true")
	}
	// SourceDataset should not be set when empty
	if _, ok := props[PropertySourceDataset]; ok {
		t.Error("PropertySourceDataset should not be set when empty")
	}
}

func TestStringToInt64(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{
			name:  "positive integer",
			input: "10737418240",
			want:  10737418240,
		},
		{
			name:  "zero",
			input: "0",
			want:  0,
		},
		{
			name:  "negative integer",
			input: "-1000000",
			want:  -1000000,
		},
		{
			name:  "empty string returns 0",
			input: "",
			want:  0,
		},
		{
			name:  "non-numeric string returns 0",
			input: "not-a-number",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StringToInt64(tt.input)
			if got != tt.want {
				t.Errorf("StringToInt64(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetSchemaVersion(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]string
		want  string
	}{
		{
			name:  "schema v1",
			props: map[string]string{PropertySchemaVersion: "1"},
			want:  "1",
		},
		{
			name:  "no schema version (legacy)",
			props: map[string]string{PropertyManagedBy: ManagedByValue},
			want:  "0",
		},
		{
			name:  "empty schema version",
			props: map[string]string{PropertySchemaVersion: ""},
			want:  "0",
		},
		{
			name:  "empty props",
			props: map[string]string{},
			want:  "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetSchemaVersion(tt.props)
			if got != tt.want {
				t.Errorf("GetSchemaVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsSchemaV1(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name  string
		props map[string]string
		want  bool
	}{
		{
			name:  "is schema v1",
			props: map[string]string{PropertySchemaVersion: "1"},
			want:  true,
		},
		{
			name:  "not schema v1 (legacy)",
			props: map[string]string{PropertyManagedBy: ManagedByValue},
			want:  false,
		},
		{
			name:  "not schema v1 (future version)",
			props: map[string]string{PropertySchemaVersion: "2"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSchemaV1(tt.props)
			if got != tt.want {
				t.Errorf("IsSchemaV1() = %v, want %v", got, tt.want)
			}
		})
	}
}
