package tnsapi

import (
	"testing"
)

func TestPropertyNames(t *testing.T) {
	names := PropertyNames()

	// Verify we have all expected properties
	expectedProps := []string{
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
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "pvc-12345678-1234-1234-1234-123456789012",
				PropertyNFSShareID:     "42",
				PropertyProtocol:       ProtocolNFS,
				PropertyProvisionedAt:  "2024-01-15T10:30:00Z",
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
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "my-persistent-volume",
				PropertyNFSShareID:     "100",
				PropertyProtocol:       ProtocolNFS,
				PropertyProvisionedAt:  "2025-06-20T14:00:00Z",
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
				PropertyManagedBy:      ManagedByValue,
				PropertyCSIVolumeName:  "test-volume",
				PropertyNFSShareID:     "0",
				PropertyProtocol:       ProtocolNFS,
				PropertyProvisionedAt:  "2024-12-01T00:00:00Z",
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
				PropertyManagedBy:        ManagedByValue,
				PropertyCSIVolumeName:    "pvc-abcdef00-1234-5678-9abc-def012345678",
				PropertyNVMeSubsystemID:  "338",
				PropertyNVMeNamespaceID:  "456",
				PropertyNVMeSubsystemNQN: "nqn.2137.csi.tns:pvc-abcdef00-1234-5678-9abc-def012345678",
				PropertyProtocol:         ProtocolNVMeOF,
				PropertyProvisionedAt:    "2024-01-15T10:30:00Z",
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
				PropertyManagedBy:        ManagedByValue,
				PropertyCSIVolumeName:    "database-volume",
				PropertyNVMeSubsystemID:  "1",
				PropertyNVMeNamespaceID:  "1",
				PropertyNVMeSubsystemNQN: "nqn.2024.io.example:database",
				PropertyProtocol:         ProtocolNVMeOF,
				PropertyProvisionedAt:    "2025-12-19T08:00:00Z",
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
}
