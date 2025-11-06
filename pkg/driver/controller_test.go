package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// mockAPIClient is a mock implementation of APIClient for testing.
type mockAPIClient struct {
	queryPoolFunc func(ctx context.Context, poolName string) (*tnsapi.Pool, error)
}

var errNotImplemented = errors.New("mock method not implemented")

func (m *mockAPIClient) CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteDataset(ctx context.Context, datasetID string) error {
	return nil
}

func (m *mockAPIClient) UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNFSShare(ctx context.Context, shareID int) error {
	return nil
}

func (m *mockAPIClient) CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error {
	return nil
}

func (m *mockAPIClient) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error {
	return nil
}

func (m *mockAPIClient) QueryNVMeOFPorts(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
	return nil, nil
}

func (m *mockAPIClient) AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error {
	return nil
}

func (m *mockAPIClient) CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	return nil
}

func (m *mockAPIClient) QuerySnapshots(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
	return nil, nil
}

func (m *mockAPIClient) CloneSnapshot(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) QueryAllDatasets(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
	return nil, nil
}

func (m *mockAPIClient) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
	return nil, nil
}

func (m *mockAPIClient) QueryAllNVMeOFNamespaces(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
	return nil, nil
}

func (m *mockAPIClient) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	if m.queryPoolFunc != nil {
		return m.queryPoolFunc(ctx, poolName)
	}
	return nil, errNotImplemented
}

func TestGetCapacity(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name              string
		params            map[string]string
		mockQueryPool     func(ctx context.Context, poolName string) (*tnsapi.Pool, error)
		wantErr           bool
		wantErrCode       codes.Code
		wantCapacity      int64
		wantEmptyResponse bool
	}{
		{
			name: "successful capacity query",
			params: map[string]string{
				"pool": "tank",
			},
			mockQueryPool: func(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
				return &tnsapi.Pool{
					ID:   1,
					Name: "tank",
					Properties: struct {
						Size struct {
							Parsed int64 `json:"parsed"`
						} `json:"size"`
						Allocated struct {
							Parsed int64 `json:"parsed"`
						} `json:"allocated"`
						Free struct {
							Parsed int64 `json:"parsed"`
						} `json:"free"`
						Capacity struct {
							Parsed int64 `json:"parsed"`
						} `json:"capacity"`
					}{
						Size: struct {
							Parsed int64 `json:"parsed"`
						}{Parsed: 1000000000000}, // 1TB
						Allocated: struct {
							Parsed int64 `json:"parsed"`
						}{Parsed: 400000000000}, // 400GB
						Free: struct {
							Parsed int64 `json:"parsed"`
						}{Parsed: 600000000000}, // 600GB
					},
				}, nil
			},
			wantErr:      false,
			wantCapacity: 600000000000, // 600GB available
		},
		{
			name:              "no parameters - returns empty response",
			params:            nil,
			wantErr:           false,
			wantEmptyResponse: true,
		},
		{
			name:              "no pool parameter - returns empty response",
			params:            map[string]string{},
			wantErr:           false,
			wantEmptyResponse: true,
		},
		{
			name: "pool query fails",
			params: map[string]string{
				"pool": "nonexistent",
			},
			mockQueryPool: func(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
				return nil, errors.New("pool not found")
			},
			wantErr:     true,
			wantErrCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock API client
			mockClient := &mockAPIClient{
				queryPoolFunc: tt.mockQueryPool,
			}

			// Create controller service
			service := NewControllerService(mockClient)

			// Create request
			req := &csi.GetCapacityRequest{
				Parameters: tt.params,
			}

			// Call GetCapacity
			resp, err := service.GetCapacity(context.Background(), req)

			// Check error expectations
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %v", err)
					return
				}
				if st.Code() != tt.wantErrCode {
					t.Errorf("Expected error code %v, got %v", tt.wantErrCode, st.Code())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if resp == nil {
				t.Fatal("GetCapacity returned nil response")
			}

			// Check empty response case
			if tt.wantEmptyResponse {
				if resp.AvailableCapacity != 0 {
					t.Errorf("Expected empty response (capacity=0), got capacity=%d", resp.AvailableCapacity)
				}
				return
			}

			// Check capacity value
			if resp.AvailableCapacity != tt.wantCapacity {
				t.Errorf("AvailableCapacity = %d, want %d", resp.AvailableCapacity, tt.wantCapacity)
			}
		})
	}
}
