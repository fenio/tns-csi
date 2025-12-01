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
	service := NewControllerService(nil, NewNodeRegistry())

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
	queryPoolFunc     func(ctx context.Context, poolName string) (*tnsapi.Pool, error)
	updateDatasetFunc func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error)
}

var errNotImplemented = errors.New("mock method not implemented")

func (m *mockAPIClient) CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteDataset(ctx context.Context, datasetID string) error {
	return nil
}

func (m *mockAPIClient) Dataset(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
	if m.updateDatasetFunc != nil {
		return m.updateDatasetFunc(ctx, datasetID, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNFSShare(ctx context.Context, shareID int) error {
	return nil
}

func (m *mockAPIClient) QueryNFSShare(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
	return nil, nil
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

func (m *mockAPIClient) NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error) {
	return nil, nil
}

func (m *mockAPIClient) ListAllNVMeOFSubsystems(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	return nil, nil
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

func (m *mockAPIClient) Close() {
	// Mock client doesn't need cleanup
}

func TestValidateCreateVolumeRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      *csi.CreateVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "valid request",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing volume name",
			req: &csi.CreateVolumeRequest{
				Name: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateVolumeRequest(tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestParseNFSShareCapacity(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    int64
	}{
		{
			name:    "pipe separator format",
			comment: "CSI Volume: test-vol | Capacity: 1073741824",
			want:    1073741824,
		},
		{
			name:    "comma separator format (legacy)",
			comment: "CSI Volume: test-vol, Capacity: 2147483648",
			want:    2147483648,
		},
		{
			name:    "empty comment",
			comment: "",
			want:    0,
		},
		{
			name:    "invalid format - no capacity",
			comment: "CSI Volume: test-vol",
			want:    0,
		},
		{
			name:    "invalid format - wrong separator",
			comment: "CSI Volume: test-vol - Capacity: 1073741824",
			want:    0,
		},
		{
			name:    "invalid capacity number",
			comment: "CSI Volume: test-vol | Capacity: invalid",
			want:    0,
		},
		{
			name:    "5GB capacity",
			comment: "CSI Volume: my-volume | Capacity: 5368709120",
			want:    5368709120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNFSShareCapacity(tt.comment)
			if got != tt.want {
				t.Errorf("parseNFSShareCapacity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateCapacityCompatibility(t *testing.T) {
	tests := []struct {
		name             string
		volumeName       string
		existingCapacity int64
		reqCapacity      int64
		wantErr          bool
		wantCode         codes.Code
	}{
		{
			name:             "matching capacities",
			volumeName:       "test-vol",
			existingCapacity: 1073741824,
			reqCapacity:      1073741824,
			wantErr:          false,
		},
		{
			name:             "existing capacity is zero (backward compatibility)",
			volumeName:       "test-vol",
			existingCapacity: 0,
			reqCapacity:      1073741824,
			wantErr:          false,
		},
		{
			name:             "mismatched capacities",
			volumeName:       "test-vol",
			existingCapacity: 1073741824,
			reqCapacity:      2147483648,
			wantErr:          true,
			wantCode:         codes.AlreadyExists,
		},
		{
			name:             "requested smaller than existing",
			volumeName:       "test-vol",
			existingCapacity: 2147483648,
			reqCapacity:      1073741824,
			wantErr:          true,
			wantCode:         codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCapacityCompatibility(tt.volumeName, tt.existingCapacity, tt.reqCapacity)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerPublishVolume(t *testing.T) {
	ctx := context.Background()

	// Create a valid encoded volume ID for testing
	volumeMeta := VolumeMetadata{
		Name:        "test-volume",
		Protocol:    ProtocolNFS,
		DatasetID:   "dataset-123",
		DatasetName: "tank/test-volume",
		NFSShareID:  42,
	}
	volumeID, err := encodeVolumeID(volumeMeta)
	if err != nil {
		t.Fatalf("Failed to encode test volume ID: %v", err)
	}

	tests := []struct {
		name     string
		req      *csi.ControllerPublishVolumeRequest
		nodeReg  *NodeRegistry
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "successful publish",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("test-node")
				return r
			}(),
			wantErr: false,
		},
		{
			name: "missing volume ID",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: "",
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg:  NewNodeRegistry(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing node ID",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg:  NewNodeRegistry(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         volumeID,
				NodeId:           "test-node",
				VolumeCapability: nil,
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("test-node")
				return r
			}(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "node not found",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "unknown-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg:  NewNodeRegistry(),
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "invalid volume ID",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: "invalid-volume-id",
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("test-node")
				return r
			}(),
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			service := NewControllerService(mockClient, tt.nodeReg)

			_, err := service.ControllerPublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		req      *csi.ControllerUnpublishVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "successful unpublish",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "test-volume-id",
			},
			wantErr: false,
		},
		{
			name: "missing volume ID",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			service := NewControllerService(mockClient, NewNodeRegistry())

			_, err := service.ControllerUnpublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	ctx := context.Background()

	// Create a valid encoded volume ID for testing
	volumeMeta := VolumeMetadata{
		Name:        "test-volume",
		Protocol:    ProtocolNFS,
		DatasetID:   "dataset-123",
		DatasetName: "tank/test-volume",
		NFSShareID:  42,
	}
	volumeID, err := encodeVolumeID(volumeMeta)
	if err != nil {
		t.Fatalf("Failed to encode test volume ID: %v", err)
	}

	tests := []struct {
		name     string
		req      *csi.ValidateVolumeCapabilitiesRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "valid capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing volume ID",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid volume ID",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: "invalid-id",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			service := NewControllerService(mockClient, NewNodeRegistry())

			resp, err := service.ValidateVolumeCapabilities(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if resp.Confirmed == nil {
				t.Error("Expected Confirmed to be non-nil")
			}
		})
	}
}

func TestControllerExpandVolume(t *testing.T) {
	ctx := context.Background()

	// Create a valid encoded volume ID for NFS testing
	nfsMeta := VolumeMetadata{
		Name:        "test-nfs-volume",
		Protocol:    ProtocolNFS,
		DatasetID:   "tank/test-nfs-volume",
		DatasetName: "tank/test-nfs-volume",
		NFSShareID:  42,
	}
	nfsVolumeID, err := encodeVolumeID(nfsMeta)
	if err != nil {
		t.Fatalf("Failed to encode NFS volume ID: %v", err)
	}

	// Create a valid encoded volume ID for NVMe-oF testing
	nvmeofMeta := VolumeMetadata{
		Name:              "test-nvmeof-volume",
		Protocol:          ProtocolNVMeOF,
		DatasetID:         "tank/test-nvmeof-volume",
		DatasetName:       "tank/test-nvmeof-volume",
		NVMeOFSubsystemID: 100,
		NVMeOFNamespaceID: 200,
	}
	nvmeofVolumeID, err := encodeVolumeID(nvmeofMeta)
	if err != nil {
		t.Fatalf("Failed to encode NVMe-oF volume ID: %v", err)
	}

	tests := []struct {
		name          string
		req           *csi.ControllerExpandVolumeRequest
		mockSetup     func(*mockAPIClient)
		checkResponse func(*testing.T, *csi.ControllerExpandVolumeResponse)
		wantErr       bool
		wantCode      codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing capacity range",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nfsVolumeID,
				CapacityRange: nil,
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid volume ID",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "invalid-id",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "NFS expansion - NodeExpansionRequired should be false",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nfsVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {
				m.updateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   datasetID,
						Name: datasetID,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be false for NFS volumes")
				}
				if resp.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.CapacityBytes)
				}
			},
		},
		{
			name: "NVMe-oF expansion - NodeExpansionRequired should be true",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nvmeofVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: 10 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {
				m.updateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   datasetID,
						Name: datasetID,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if !resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be true for NVMe-oF volumes")
				}
				if resp.CapacityBytes != 10*1024*1024*1024 {
					t.Errorf("Expected capacity 10GB, got %d", resp.CapacityBytes)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)
			service := NewControllerService(mockClient, NewNodeRegistry())

			resp, err := service.ControllerExpandVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
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
			service := NewControllerService(mockClient, NewNodeRegistry())

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

func TestNodeRegistryUnregisterAndCount(t *testing.T) {
	t.Run("basic operations", func(t *testing.T) {
		registry := NewNodeRegistry()

		// Initially empty
		if registry.Count() != 0 {
			t.Errorf("Expected count 0, got %d", registry.Count())
		}

		// Register nodes
		registry.Register("node1")
		if registry.Count() != 1 {
			t.Errorf("Expected count 1, got %d", registry.Count())
		}
		if !registry.IsRegistered("node1") {
			t.Error("node1 should be registered")
		}

		registry.Register("node2")
		if registry.Count() != 2 {
			t.Errorf("Expected count 2, got %d", registry.Count())
		}

		registry.Register("node3")
		if registry.Count() != 3 {
			t.Errorf("Expected count 3, got %d", registry.Count())
		}

		// Unregister one
		registry.Unregister("node2")
		if registry.Count() != 2 {
			t.Errorf("Expected count 2 after unregister, got %d", registry.Count())
		}
		if registry.IsRegistered("node2") {
			t.Error("node2 should not be registered after unregister")
		}

		// Other nodes should still be there
		if !registry.IsRegistered("node1") {
			t.Error("node1 should still be registered")
		}
		if !registry.IsRegistered("node3") {
			t.Error("node3 should still be registered")
		}

		// Unregister nonexistent node (should not panic)
		registry.Unregister("nonexistent")
		if registry.Count() != 2 {
			t.Errorf("Count should still be 2 after unregistering nonexistent, got %d", registry.Count())
		}

		// Unregister all
		registry.Unregister("node1")
		registry.Unregister("node3")
		if registry.Count() != 0 {
			t.Errorf("Expected count 0 after unregistering all, got %d", registry.Count())
		}
	})

	t.Run("re-registration", func(t *testing.T) {
		registry := NewNodeRegistry()

		registry.Register("node1")
		registry.Unregister("node1")

		// Should be able to re-register
		registry.Register("node1")
		if !registry.IsRegistered("node1") {
			t.Error("node1 should be registered after re-registration")
		}
		if registry.Count() != 1 {
			t.Errorf("Expected count 1, got %d", registry.Count())
		}
	})

	t.Run("duplicate registration", func(t *testing.T) {
		registry := NewNodeRegistry()

		registry.Register("node1")
		registry.Register("node1") // Duplicate

		// Count should still be 1 (map overwrites)
		if registry.Count() != 1 {
			t.Errorf("Expected count 1 after duplicate registration, got %d", registry.Count())
		}
	})
}

func TestNVMeOFNamespaceRegistry(t *testing.T) {
	t.Run("basic operations", func(t *testing.T) {
		registry := NewNVMeOFNamespaceRegistry()

		nqn := "nqn.2005-03.org.truenas:csi-test"
		nsid := "1"

		// Initially zero
		if registry.NQNCount(nqn) != 0 {
			t.Errorf("Expected NQN count 0, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, nsid) != 0 {
			t.Errorf("Expected namespace count 0, got %d", registry.NamespaceCount(nqn, nsid))
		}

		// Register namespace
		registry.Register(nqn, nsid)
		if registry.NQNCount(nqn) != 1 {
			t.Errorf("Expected NQN count 1, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, nsid) != 1 {
			t.Errorf("Expected namespace count 1, got %d", registry.NamespaceCount(nqn, nsid))
		}

		// Register same namespace again (reference count)
		registry.Register(nqn, nsid)
		if registry.NQNCount(nqn) != 2 {
			t.Errorf("Expected NQN count 2, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, nsid) != 2 {
			t.Errorf("Expected namespace count 2, got %d", registry.NamespaceCount(nqn, nsid))
		}

		// Unregister once (should not be last)
		isLast := registry.Unregister(nqn, nsid)
		if isLast {
			t.Error("Should not be last namespace yet")
		}
		if registry.NQNCount(nqn) != 1 {
			t.Errorf("Expected NQN count 1, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, nsid) != 1 {
			t.Errorf("Expected namespace count 1, got %d", registry.NamespaceCount(nqn, nsid))
		}

		// Unregister again (should be last)
		isLast = registry.Unregister(nqn, nsid)
		if !isLast {
			t.Error("Should be last namespace")
		}
		if registry.NQNCount(nqn) != 0 {
			t.Errorf("Expected NQN count 0, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, nsid) != 0 {
			t.Errorf("Expected namespace count 0, got %d", registry.NamespaceCount(nqn, nsid))
		}
	})

	t.Run("multiple namespaces same NQN", func(t *testing.T) {
		registry := NewNVMeOFNamespaceRegistry()

		nqn := "nqn.2005-03.org.truenas:csi-test"

		// Register different namespaces for the same NQN
		registry.Register(nqn, "1")
		registry.Register(nqn, "2")
		registry.Register(nqn, "3")

		if registry.NQNCount(nqn) != 3 {
			t.Errorf("Expected NQN count 3, got %d", registry.NQNCount(nqn))
		}

		// Each namespace has count 1
		if registry.NamespaceCount(nqn, "1") != 1 {
			t.Errorf("Expected namespace 1 count 1, got %d", registry.NamespaceCount(nqn, "1"))
		}
		if registry.NamespaceCount(nqn, "2") != 1 {
			t.Errorf("Expected namespace 2 count 1, got %d", registry.NamespaceCount(nqn, "2"))
		}
		if registry.NamespaceCount(nqn, "3") != 1 {
			t.Errorf("Expected namespace 3 count 1, got %d", registry.NamespaceCount(nqn, "3"))
		}

		// Unregister namespace 2
		isLast := registry.Unregister(nqn, "2")
		if isLast {
			t.Error("Should not be last (still have 1 and 3)")
		}
		if registry.NQNCount(nqn) != 2 {
			t.Errorf("Expected NQN count 2, got %d", registry.NQNCount(nqn))
		}
		if registry.NamespaceCount(nqn, "2") != 0 {
			t.Errorf("Expected namespace 2 count 0, got %d", registry.NamespaceCount(nqn, "2"))
		}

		// Unregister namespace 1
		isLast = registry.Unregister(nqn, "1")
		if isLast {
			t.Error("Should not be last (still have 3)")
		}

		// Unregister namespace 3 (last one)
		isLast = registry.Unregister(nqn, "3")
		if !isLast {
			t.Error("Should be last namespace")
		}
		if registry.NQNCount(nqn) != 0 {
			t.Errorf("Expected NQN count 0, got %d", registry.NQNCount(nqn))
		}
	})

	t.Run("multiple NQNs", func(t *testing.T) {
		registry := NewNVMeOFNamespaceRegistry()

		nqn1 := "nqn.2005-03.org.truenas:csi-test1"
		nqn2 := "nqn.2005-03.org.truenas:csi-test2"

		registry.Register(nqn1, "1")
		registry.Register(nqn2, "1")

		if registry.NQNCount(nqn1) != 1 {
			t.Errorf("Expected NQN1 count 1, got %d", registry.NQNCount(nqn1))
		}
		if registry.NQNCount(nqn2) != 1 {
			t.Errorf("Expected NQN2 count 1, got %d", registry.NQNCount(nqn2))
		}

		// Unregister from NQN1 should not affect NQN2
		isLast := registry.Unregister(nqn1, "1")
		if !isLast {
			t.Error("Should be last for NQN1")
		}
		if registry.NQNCount(nqn1) != 0 {
			t.Errorf("Expected NQN1 count 0, got %d", registry.NQNCount(nqn1))
		}
		if registry.NQNCount(nqn2) != 1 {
			t.Errorf("Expected NQN2 count still 1, got %d", registry.NQNCount(nqn2))
		}
	})

	t.Run("unregister nonexistent", func(t *testing.T) {
		registry := NewNVMeOFNamespaceRegistry()

		// Unregister something that was never registered
		isLast := registry.Unregister("nqn.nonexistent", "1")
		if isLast {
			t.Error("Should return false for nonexistent NQN")
		}
	})
}

func TestCreateVolumeRPC(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		req           *csi.CreateVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		wantErr       bool
		wantCode      codes.Code
	}{
		{
			name: "successful NFS volume creation via RPC",
			req: &csi.CreateVolumeRequest{
				Name: "test-rpc-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":      "nfs",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/csi",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/csi/test-rpc-volume",
						Name:       "tank/csi/test-rpc-volume",
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/csi/test-rpc-volume",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return &tnsapi.NFSShare{
						ID:      1,
						Path:    "/mnt/tank/csi/test-rpc-volume",
						Enabled: true,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateVolumeResponse) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId == "" {
					t.Error("Expected volume ID to be non-empty")
				}
			},
		},
		{
			name: "validation failure - missing name",
			req: &csi.CreateVolumeRequest{
				Name: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "validation failure - missing capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "unsupported protocol",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "unsupported-protocol",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "default protocol (NFS) when not specified",
			req: &csi.CreateVolumeRequest{
				Name: "test-default-protocol",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"pool":   "tank",
					"server": "192.168.1.100",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/test-default-protocol",
						Name:       "tank/test-default-protocol",
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/test-default-protocol",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return &tnsapi.NFSShare{
						ID:      2,
						Path:    "/mnt/tank/test-default-protocol",
						Enabled: true,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateVolumeResponse) {
				t.Helper()
				// Should succeed with NFS as default protocol
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry())
			resp, err := service.CreateVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestDeleteVolumeRPC(t *testing.T) {
	ctx := context.Background()

	// Create a valid NFS volume ID
	nfsMeta := VolumeMetadata{
		Name:        "test-delete-volume",
		Protocol:    ProtocolNFS,
		DatasetID:   "tank/csi/test-delete-volume",
		DatasetName: "tank/csi/test-delete-volume",
		NFSShareID:  42,
	}
	nfsVolumeID, err := encodeVolumeID(nfsMeta)
	if err != nil {
		t.Fatalf("Failed to encode volume ID: %v", err)
	}

	// Create a valid NVMe-oF volume ID
	nvmeofMeta := VolumeMetadata{
		Name:              "test-delete-nvmeof",
		Protocol:          ProtocolNVMeOF,
		DatasetID:         "tank/csi/test-delete-nvmeof",
		DatasetName:       "tank/csi/test-delete-nvmeof",
		NVMeOFSubsystemID: 10,
		NVMeOFNamespaceID: 20,
		NVMeOFNQN:         "nqn.2005-03.org.truenas:test",
	}
	nvmeofVolumeID, err := encodeVolumeID(nvmeofMeta)
	if err != nil {
		t.Fatalf("Failed to encode NVMe-oF volume ID: %v", err)
	}

	tests := []struct {
		name      string
		req       *csi.DeleteVolumeRequest
		mockSetup func(*MockAPIClientForSnapshots)
		wantErr   bool
		wantCode  codes.Code
	}{
		{
			name: "successful NFS volume deletion",
			req: &csi.DeleteVolumeRequest{
				VolumeId: nfsVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					if shareID != 42 {
						return errors.New("unexpected share ID")
					}
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "successful NVMe-oF volume deletion",
			req: &csi.DeleteVolumeRequest{
				VolumeId: nvmeofVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					return nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					// No remaining namespaces
					return []tnsapi.NVMeOFNamespace{}, nil
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "missing volume ID",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid volume ID format - idempotent success",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "invalid-id",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   false, // DeleteVolume is idempotent - returns success for invalid/non-existent volumes
		},
		{
			name: "idempotent - volume already deleted",
			req: &csi.DeleteVolumeRequest{
				VolumeId: nfsVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					// Simulate share already deleted - not found
					return errors.New("share not found")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					// Simulate dataset already deleted - not found
					return errors.New("dataset does not exist")
				}
			},
			wantErr: false, // DeleteVolume should be idempotent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry())
			_, err := service.DeleteVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestListVolumes(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		req           *csi.ListVolumesRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.ListVolumesResponse)
		wantErr       bool
		wantCode      codes.Code
	}{
		{
			name: "list volumes - empty",
			req:  &csi.ListVolumesRequest{},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ListVolumesResponse) {
				t.Helper()
				if len(resp.Entries) != 0 {
					t.Errorf("Expected 0 entries, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "list volumes with pagination token - token not found",
			req: &csi.ListVolumesRequest{
				StartingToken: "nonexistent-volume-id",
				MaxEntries:    5,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
			},
			wantErr:  true, // Token not found in volume list
			wantCode: codes.Aborted,
		},
		{
			name: "list volumes - API failure",
			req: &csi.ListVolumesRequest{
				StartingToken: "some-token",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return nil, errors.New("API error")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry())
			resp, err := service.ListVolumes(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}
