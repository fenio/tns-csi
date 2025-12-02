package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func (m *mockAPIClient) PromoteDataset(ctx context.Context, datasetID string) error {
	return nil
}

func (m *mockAPIClient) DatasetDestroySnapshots(ctx context.Context, datasetID string) error {
	return nil
}

func (m *mockAPIClient) CreateDetachedClone(ctx context.Context, snapshotID, targetDataset string, timeout time.Duration) (*tnsapi.Dataset, error) {
	return nil, errNotImplemented
}

func (m *mockAPIClient) CoreGetJobs(ctx context.Context, filters []interface{}) ([]tnsapi.Job, error) {
	return nil, nil
}

//nolint:nilnil // Mock method - nil return is acceptable for unused mock
func (m *mockAPIClient) CoreGetJob(ctx context.Context, jobID int) (*tnsapi.Job, error) {
	return nil, nil
}

//nolint:nilnil // Mock method - nil return is acceptable for unused mock
func (m *mockAPIClient) CoreWaitForJob(ctx context.Context, jobID int, timeout time.Duration) (*tnsapi.Job, error) {
	return nil, nil
}

func (m *mockAPIClient) ReplicationRunOnetime(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error) {
	return 0, nil
}

func TestValidateCreateVolumeRequest(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
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

	// Use plain volume ID (CSI spec compliant - under 128 bytes)
	volumeID := "test-volume"

	//nolint:govet // Field alignment not critical for test structs
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
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: ProtocolNFS,
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

	//nolint:govet // Field alignment not critical for test structs
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

	// Use plain volume ID (CSI spec compliant - under 128 bytes)
	volumeID := "test-volume"

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name      string
		req       *csi.ValidateVolumeCapabilitiesRequest
		mockSetup func(m *MockAPIClientForSnapshots)
		wantErr   bool
		wantCode  codes.Code
	}{
		{
			name: "valid capabilities - volume exists",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: ProtocolNFS,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock finding the NFS share
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:   1,
							Path: "/mnt/tank/" + volumeID,
						},
					}, nil
				}
			},
			wantErr: false,
		},
		{
			name: "volume not found",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: "non-existent-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock returning empty results - volume not found
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.NotFound,
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
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: nil,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "empty capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)
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

	// Use plain volume IDs (CSI spec compliant - under 128 bytes)
	nfsVolumeID := "test-nfs-volume"
	nvmeofVolumeID := "test-nvmeof-volume"

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name          string
		req           *csi.ControllerExpandVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
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
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing capacity range",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nfsVolumeID,
				CapacityRange: nil,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "NFS expansion - NodeExpansionRequired should be false",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nfsVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock finding the NFS share
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:   42,
							Path: "/mnt/tank/csi/" + nfsVolumeID,
						},
					}, nil
				}
				// Mock finding the dataset
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:   "tank/csi/" + nfsVolumeID,
							Name: "tank/csi/" + nfsVolumeID,
						},
					}, nil
				}
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
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
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock NFS shares returning nothing for NVMe-oF volume
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				// Mock finding the NVMe-oF namespace
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{
						{
							ID:        200,
							Subsystem: 100,
							Device:    "/dev/zvol/tank/csi/" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock finding the subsystem
				m.ListAllNVMeOFSubsystemsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{
						{
							ID:  100,
							NQN: "nqn.2005-03.org.truenas:test",
						},
					}, nil
				}
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
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
		{
			name: "volume not found",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "nonexistent-volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
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

func TestCreateVolumeRPC(t *testing.T) {
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
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

	// Use plain volume IDs (CSI spec compliant - under 128 bytes)
	nfsVolumeID := "test-delete-volume"
	nvmeofVolumeID := "test-delete-nvmeof"

	//nolint:govet // Field alignment not critical for test structs
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
				Secrets: map[string]string{
					VolumeContextKeyProtocol:    ProtocolNFS,
					VolumeContextKeyDatasetName: "tank/csi/test-delete-volume",
					VolumeContextKeyNFSShareID:  "42",
				},
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
				Secrets: map[string]string{
					VolumeContextKeyProtocol:          ProtocolNVMeOF,
					VolumeContextKeyDatasetName:       "tank/csi/test-delete-nvmeof",
					VolumeContextKeyNVMeOFSubsystemID: "10",
					VolumeContextKeyNVMeOFNamespaceID: "20",
					VolumeContextKeyNQN:               "nqn.2005-03.org.truenas:test",
				},
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
			name: "volume deletion with missing secrets - idempotent success",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "unknown-volume",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   false, // DeleteVolume is idempotent - returns success for volumes without metadata
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

	//nolint:govet // Field alignment not critical for test structs
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
