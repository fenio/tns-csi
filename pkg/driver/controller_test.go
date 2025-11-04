package driver

import (
	"context"
	"testing"
)

func TestEncodeDecodeVolumeID(t *testing.T) {
	tests := []struct {
		name    string
		meta    VolumeMetadata
		wantErr bool
	}{
		{
			name: "NFS volume metadata",
			meta: VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    "nfs",
				DatasetID:   "dataset-123",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  42,
			},
			wantErr: false,
		},
		{
			name: "NVMe-oF volume metadata",
			meta: VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          "nvmeof",
				DatasetID:         "zvol-456",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 10,
				NVMeOFNamespaceID: 20,
				NVMeOFNQN:         "nqn.2014-08.org.nvmexpress:uuid:test-uuid",
			},
			wantErr: false,
		},
		{
			name: "Minimal metadata",
			meta: VolumeMetadata{
				Name:     "minimal",
				Protocol: "nfs",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode the metadata
			encoded, err := encodeVolumeID(tt.meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("encodeVolumeID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			// Verify encoded string is not empty
			if encoded == "" {
				t.Errorf("encodeVolumeID() returned empty string")
				return
			}

			// Decode the encoded string
			decoded, err := decodeVolumeID(encoded)
			if err != nil {
				t.Errorf("decodeVolumeID() error = %v", err)
				return
			}

			// Verify decoded metadata matches original
			if decoded.Name != tt.meta.Name {
				t.Errorf("Name = %v, want %v", decoded.Name, tt.meta.Name)
			}
			if decoded.Protocol != tt.meta.Protocol {
				t.Errorf("Protocol = %v, want %v", decoded.Protocol, tt.meta.Protocol)
			}
			if decoded.DatasetID != tt.meta.DatasetID {
				t.Errorf("DatasetID = %v, want %v", decoded.DatasetID, tt.meta.DatasetID)
			}
			if decoded.DatasetName != tt.meta.DatasetName {
				t.Errorf("DatasetName = %v, want %v", decoded.DatasetName, tt.meta.DatasetName)
			}
			if decoded.NFSShareID != tt.meta.NFSShareID {
				t.Errorf("NFSShareID = %v, want %v", decoded.NFSShareID, tt.meta.NFSShareID)
			}
			if decoded.NVMeOFSubsystemID != tt.meta.NVMeOFSubsystemID {
				t.Errorf("NVMeOFSubsystemID = %v, want %v", decoded.NVMeOFSubsystemID, tt.meta.NVMeOFSubsystemID)
			}
			if decoded.NVMeOFNamespaceID != tt.meta.NVMeOFNamespaceID {
				t.Errorf("NVMeOFNamespaceID = %v, want %v", decoded.NVMeOFNamespaceID, tt.meta.NVMeOFNamespaceID)
			}
			if decoded.NVMeOFNQN != tt.meta.NVMeOFNQN {
				t.Errorf("NVMeOFNQN = %v, want %v", decoded.NVMeOFNQN, tt.meta.NVMeOFNQN)
			}
		})
	}
}

func TestDecodeVolumeID_InvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		volumeID string
		wantErr  bool
	}{
		{
			name:     "Invalid base64",
			volumeID: "!!!invalid!!!",
			wantErr:  true,
		},
		{
			name:     "Valid base64 but invalid JSON",
			volumeID: "bm90LWpzb24",
			wantErr:  true,
		},
		{
			name:     "Empty string",
			volumeID: "",
			wantErr:  true,
		},
		{
			name:     "Legacy format (with slashes)",
			volumeID: "tank/my-volume",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeVolumeID(tt.volumeID)
			if (err != nil) != tt.wantErr {
				t.Errorf("decodeVolumeID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsEncodedVolumeID(t *testing.T) {
	tests := []struct {
		name     string
		volumeID string
		want     bool
	}{
		{
			name:     "Valid base64 URL-safe string",
			volumeID: "eyJuYW1lIjoidGVzdCJ9",
			want:     true,
		},
		{
			name:     "Contains invalid characters",
			volumeID: "tank/volume",
			want:     false,
		},
		{
			name:     "Contains spaces",
			volumeID: "test volume",
			want:     false,
		},
		{
			name:     "Empty string",
			volumeID: "",
			want:     true, // Empty string is technically valid base64
		},
		{
			name:     "Alphanumeric only",
			volumeID: "abc123",
			want:     true,
		},
		{
			name:     "With URL-safe characters",
			volumeID: "abc-123_def",
			want:     true,
		},
		{
			name:     "With standard base64 padding (not URL-safe)",
			volumeID: "abc123==",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isEncodedVolumeID(tt.volumeID)
			if got != tt.want {
				t.Errorf("isEncodedVolumeID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	service := NewControllerService(nil)

	resp, err := service.ControllerGetCapabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ControllerGetCapabilities() error = %v", err)
	}

	if resp == nil {
		t.Fatal("ControllerGetCapabilities() returned nil response")
	}

	if len(resp.Capabilities) == 0 {
		t.Error("ControllerGetCapabilities() returned no capabilities")
	}

	// Verify expected capabilities are present
	expectedCaps := map[string]bool{
		"CREATE_DELETE_VOLUME":     false,
		"PUBLISH_UNPUBLISH_VOLUME": false,
		"LIST_VOLUMES":             false,
		"GET_CAPACITY":             false,
	}

	for _, cap := range resp.Capabilities {
		if rpc := cap.GetRpc(); rpc != nil {
			switch rpc.Type.String() {
			case "CREATE_DELETE_VOLUME":
				expectedCaps["CREATE_DELETE_VOLUME"] = true
			case "PUBLISH_UNPUBLISH_VOLUME":
				expectedCaps["PUBLISH_UNPUBLISH_VOLUME"] = true
			case "LIST_VOLUMES":
				expectedCaps["LIST_VOLUMES"] = true
			case "GET_CAPACITY":
				expectedCaps["GET_CAPACITY"] = true
			}
		}
	}

	for cap, found := range expectedCaps {
		if !found {
			t.Errorf("Expected capability %s not found", cap)
		}
	}
}
