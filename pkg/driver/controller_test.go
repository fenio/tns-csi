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

	// Use require pattern - fail immediately if nil.
	requireNotNilController(t, resp, "ControllerGetCapabilities() returned nil response")

	if len(resp.Capabilities) == 0 {
		t.Error("ControllerGetCapabilities() returned no capabilities")
	}

	// Verify expected capabilities are present.
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

// requireNotNilController fails the test immediately if v is nil.
// This helper avoids staticcheck SA5011 warnings about nil pointer dereference
// that occur when using the pattern: if x == nil { t.Fatal(...) }; x.Field.
func requireNotNilController(t *testing.T, v any, msg string) {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
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

func (m *mockAPIClient) RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error {
	return nil
}

func (m *mockAPIClient) QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error) {
	return nil, nil
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

func (m *mockAPIClient) PromoteDataset(ctx context.Context, datasetID string) error {
	return nil // Stub implementation - always succeed
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

func (m *mockAPIClient) SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error {
	return nil // Stub implementation
}

func (m *mockAPIClient) SetSnapshotProperties(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error {
	return nil // Stub implementation
}

func (m *mockAPIClient) GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error) {
	return make(map[string]string), nil // Stub implementation - returns empty properties
}

func (m *mockAPIClient) GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error) {
	return make(map[string]string), nil // Stub implementation - returns empty properties
}

func (m *mockAPIClient) InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error {
	return nil // Stub implementation
}

func (m *mockAPIClient) ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error {
	return nil // Stub implementation
}

// Replication methods for detached snapshots.
func (m *mockAPIClient) RunOnetimeReplication(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error) {
	return 12345, nil // Stub implementation
}

func (m *mockAPIClient) GetJobStatus(ctx context.Context, jobID int) (*tnsapi.ReplicationJobState, error) {
	return &tnsapi.ReplicationJobState{
		ID:       jobID,
		State:    "SUCCESS",
		Progress: map[string]interface{}{"percent": float64(100)},
	}, nil // Stub implementation
}

func (m *mockAPIClient) WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error {
	return nil // Stub implementation
}

func (m *mockAPIClient) RunOnetimeReplicationAndWait(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams, pollInterval time.Duration) error {
	return nil // Stub implementation
}

func (m *mockAPIClient) FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error) {
	return nil, nil // Stub implementation - returns empty result
}

func (m *mockAPIClient) FindManagedDatasets(ctx context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error) {
	return nil, nil // Stub implementation - returns empty result
}

func (m *mockAPIClient) FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*tnsapi.DatasetWithProperties, error) {
	return nil, nil //nolint:nilnil // Stub implementation - returns "not found"
}

func (m *mockAPIClient) Close() {
	// Mock client doesn't need cleanup
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
		{
			name: "capacity below minimum (TrueNAS enforces 1 GiB minimum)",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 500 * 1024 * 1024, // 500 MiB - below 1 GiB minimum
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "capacity at minimum (1 GiB)",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024, // 1 GiB - exactly at minimum
				},
			},
			wantErr: false,
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
			name:    "invalid format - comma separator (legacy format no longer supported)",
			comment: "CSI Volume: test-vol, Capacity: 2147483648",
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
			name: "capacity below minimum (TrueNAS enforces 1 GiB minimum)",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      nfsVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: 500 * 1024 * 1024}, // 500 MiB - below 1 GiB minimum
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
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nfsVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nfsVolumeID,
								Name: "tank/csi/" + nfsVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:  {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:   {Value: tnsapi.ProtocolNFS},
								tnsapi.PropertyNFSShareID: {Value: "42"},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
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
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nvmeofVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nvmeofVolumeID,
								Name: "tank/csi/" + nvmeofVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
								tnsapi.PropertyNVMeSubsystemID:  {Value: "100"},
								tnsapi.PropertyNVMeNamespaceID:  {Value: "200"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2005-03.org.truenas:test"},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
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
				// Return nil from property-based lookup (volume not found)
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					return nil, nil //nolint:nilnil // intentional: volume not found
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

func TestControllerGetVolume(t *testing.T) {
	ctx := context.Background()

	// Use plain volume IDs (CSI spec compliant - under 128 bytes)
	nfsVolumeID := "test-nfs-volume"
	nvmeofVolumeID := "test-nvmeof-volume"

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name          string
		req           *csi.ControllerGetVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.ControllerGetVolumeResponse)
		wantErr       bool
		wantCode      codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: "",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "volume not found",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: "nonexistent-volume",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Return nil from property-based lookup (volume not found)
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "healthy NFS volume",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nfsVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nfsVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:         "tank/csi/" + nfsVolumeID,
								Name:       "tank/csi/" + nfsVolumeID,
								Type:       "FILESYSTEM",
								Mountpoint: "/mnt/tank/csi/" + nfsVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:  {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:   {Value: tnsapi.ProtocolNFS},
								tnsapi.PropertyNFSShareID: {Value: "42"},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock NFS shares for health check
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:      42,
							Path:    "/mnt/tank/csi/" + nfsVolumeID,
							Enabled: true,
						},
					}, nil
				}
				// Mock Dataset() for getNFSVolumeInfo health check
				m.GetDatasetFunc = func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/csi/" + nfsVolumeID,
						Name:       "tank/csi/" + nfsVolumeID,
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/csi/" + nfsVolumeID,
						Available:  map[string]interface{}{"parsed": float64(5368709120)}, // 5GB
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId != nfsVolumeID {
					t.Errorf("Expected volume ID %s, got %s", nfsVolumeID, resp.Volume.VolumeId)
				}
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if resp.Status.VolumeCondition.Abnormal {
					t.Errorf("Expected Abnormal to be false, got true with message: %s", resp.Status.VolumeCondition.Message)
				}
				if resp.Status.VolumeCondition.Message != msgVolumeIsHealthy {
					t.Errorf("Expected message '%s', got '%s'", msgVolumeIsHealthy, resp.Status.VolumeCondition.Message)
				}
			},
		},
		{
			name: "NFS volume with missing dataset",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nfsVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nfsVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nfsVolumeID,
								Name: "tank/csi/" + nfsVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:  {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:   {Value: tnsapi.ProtocolNFS},
								tnsapi.PropertyNFSShareID: {Value: "42"},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock NFS shares for health check
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:      42,
							Path:    "/mnt/tank/csi/" + nfsVolumeID,
							Enabled: true,
						},
					}, nil
				}
				// Mock Dataset() for getNFSVolumeInfo health check - returns error to simulate missing dataset
				m.GetDatasetFunc = func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
					return nil, errors.New("dataset not found")
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if !resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected Abnormal to be true for missing dataset")
				}
				if resp.Status.VolumeCondition.Message == "" {
					t.Error("Expected non-empty error message")
				}
			},
		},
		{
			name: "NFS volume with disabled share",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nfsVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nfsVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nfsVolumeID,
								Name: "tank/csi/" + nfsVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:  {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:   {Value: tnsapi.ProtocolNFS},
								tnsapi.PropertyNFSShareID: {Value: "42"},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock finding the NFS share (disabled)
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:      42,
							Path:    "/mnt/tank/csi/" + nfsVolumeID,
							Enabled: false, // Share is disabled
						},
					}, nil
				}
				// Mock Dataset() for getNFSVolumeInfo health check
				m.GetDatasetFunc = func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/csi/" + nfsVolumeID,
						Name:       "tank/csi/" + nfsVolumeID,
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/csi/" + nfsVolumeID,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if !resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected Abnormal to be true for disabled share")
				}
				if resp.Status.VolumeCondition.Message == "" {
					t.Error("Expected non-empty error message")
				}
			},
		},
		{
			name: "healthy NVMe-oF volume",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nvmeofVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nvmeofVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nvmeofVolumeID,
								Name: "tank/csi/" + nvmeofVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
								tnsapi.PropertyNVMeSubsystemID:  {Value: "100"},
								tnsapi.PropertyNVMeNamespaceID:  {Value: "200"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock finding the NVMe-oF namespace for health check
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{
						{
							ID:     200,
							Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: 100, Name: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							Device: "/dev/zvol/tank/csi/" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock finding the subsystem for health check
				m.ListAllNVMeOFSubsystemsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{
						{
							ID:  100,
							NQN: "nqn.2005-03.org.truenas:" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock ZVOL exists for health check
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:      "tank/csi/" + nvmeofVolumeID,
							Name:    "tank/csi/" + nvmeofVolumeID,
							Type:    "VOLUME",
							Volsize: map[string]interface{}{"parsed": float64(10737418240)}, // 10GB
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId != nvmeofVolumeID {
					t.Errorf("Expected volume ID %s, got %s", nvmeofVolumeID, resp.Volume.VolumeId)
				}
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if resp.Status.VolumeCondition.Abnormal {
					t.Errorf("Expected Abnormal to be false, got true with message: %s", resp.Status.VolumeCondition.Message)
				}
				if resp.Status.VolumeCondition.Message != msgVolumeIsHealthy {
					t.Errorf("Expected message '%s', got '%s'", msgVolumeIsHealthy, resp.Status.VolumeCondition.Message)
				}
			},
		},
		{
			name: "NVMe-oF volume with missing ZVOL",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nvmeofVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nvmeofVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nvmeofVolumeID,
								Name: "tank/csi/" + nvmeofVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
								tnsapi.PropertyNVMeSubsystemID:  {Value: "100"},
								tnsapi.PropertyNVMeNamespaceID:  {Value: "200"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock finding the NVMe-oF namespace for health check
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{
						{
							ID:     200,
							Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: 100, Name: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							Device: "/dev/zvol/tank/csi/" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock finding the subsystem for health check
				m.ListAllNVMeOFSubsystemsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{
						{
							ID:  100,
							NQN: "nqn.2005-03.org.truenas:" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock ZVOL not found
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil // Empty - ZVOL not found
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if !resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected Abnormal to be true for missing ZVOL")
				}
				if resp.Status.VolumeCondition.Message == "" {
					t.Error("Expected non-empty error message")
				}
			},
		},
		{
			name: "NVMe-oF volume with missing subsystem",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nvmeofVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nvmeofVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nvmeofVolumeID,
								Name: "tank/csi/" + nvmeofVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
								tnsapi.PropertyNVMeSubsystemID:  {Value: "100"},
								tnsapi.PropertyNVMeNamespaceID:  {Value: "200"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock finding the NVMe-oF namespace for health check
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{
						{
							ID:     200,
							Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: 100, Name: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							Device: "/dev/zvol/tank/csi/" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock subsystem not found (empty list)
				m.ListAllNVMeOFSubsystemsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{}, nil // Empty - no subsystems
				}
				// Mock ZVOL exists
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:   "tank/csi/" + nvmeofVolumeID,
							Name: "tank/csi/" + nvmeofVolumeID,
							Type: "VOLUME",
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				if !resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected Abnormal to be true for missing subsystem")
				}
				if resp.Status.VolumeCondition.Message == "" {
					t.Error("Expected non-empty error message")
				}
			},
		},
		{
			name: "NVMe-oF volume with missing namespace",
			req: &csi.ControllerGetVolumeRequest{
				VolumeId: nvmeofVolumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Mock property-based lookup
				m.FindDatasetByCSIVolumeNameFunc = func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
					if volumeName == nvmeofVolumeID {
						return &tnsapi.DatasetWithProperties{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/" + nvmeofVolumeID,
								Name: "tank/csi/" + nvmeofVolumeID,
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
								tnsapi.PropertyNVMeSubsystemID:  {Value: "100"},
								tnsapi.PropertyNVMeNamespaceID:  {Value: "200"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							},
						}, nil
					}
					return nil, nil //nolint:nilnil // intentional: volume not found
				}
				// Mock finding the NVMe-oF namespace with proper subsystem info
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{
						{
							ID:     200,
							Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: 100, Name: "nqn.2005-03.org.truenas:" + nvmeofVolumeID},
							Device: "/dev/zvol/tank/csi/" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock finding the subsystem
				m.ListAllNVMeOFSubsystemsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{
						{
							ID:  100,
							NQN: "nqn.2005-03.org.truenas:" + nvmeofVolumeID,
						},
					}, nil
				}
				// Mock ZVOL exists
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:   "tank/csi/" + nvmeofVolumeID,
							Name: "tank/csi/" + nvmeofVolumeID,
							Type: "VOLUME",
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				// This test verifies health check with namespace present
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status with condition to be non-nil")
					return
				}
				// Since the namespace is found in the health check, volume should be healthy
				if resp.Status.VolumeCondition.Abnormal {
					t.Errorf("Expected healthy volume but got abnormal: %s", resp.Status.VolumeCondition.Message)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry())
			resp, err := service.ControllerGetVolume(ctx, tt.req)

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

// TestIsVolumeAdoptable tests the IsVolumeAdoptable function.
func TestIsVolumeAdoptable(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		name  string
		props map[string]tnsapi.UserProperty
		want  bool
	}{
		{
			name: "valid NFS volume with all properties",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
				tnsapi.PropertySchemaVersion:  {Value: tnsapi.SchemaVersionV1},
				tnsapi.PropertyProtocol:       {Value: tnsapi.ProtocolNFS},
				tnsapi.PropertyNFSSharePath:   {Value: "/mnt/tank/csi/pvc-123"},
				tnsapi.PropertyCSIVolumeName:  {Value: "pvc-123"},
				tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
				tnsapi.PropertyDeleteStrategy: {Value: tnsapi.DeleteStrategyDelete},
			},
			want: true,
		},
		{
			name: "valid NVMe-oF volume with all properties",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
				tnsapi.PropertySchemaVersion:    {Value: tnsapi.SchemaVersionV1},
				tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNVMeOF},
				tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2024.io.truenas:nvme:pvc-123"},
				tnsapi.PropertyCSIVolumeName:    {Value: "pvc-123"},
			},
			want: true,
		},
		{
			name: "NFS volume without schema version (still valid)",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:    {Value: tnsapi.ManagedByValue},
				tnsapi.PropertyProtocol:     {Value: tnsapi.ProtocolNFS},
				tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-123"},
			},
			want: true,
		},
		{
			name: "missing managed_by property",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyProtocol:     {Value: tnsapi.ProtocolNFS},
				tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-123"},
			},
			want: false,
		},
		{
			name: "wrong managed_by value",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:    {Value: "other-csi-driver"},
				tnsapi.PropertyProtocol:     {Value: tnsapi.ProtocolNFS},
				tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-123"},
			},
			want: false,
		},
		{
			name: "unknown schema version",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:     {Value: tnsapi.ManagedByValue},
				tnsapi.PropertySchemaVersion: {Value: "2"}, // Unknown version
				tnsapi.PropertyProtocol:      {Value: tnsapi.ProtocolNFS},
				tnsapi.PropertyNFSSharePath:  {Value: "/mnt/tank/csi/pvc-123"},
			},
			want: false,
		},
		{
			name: "missing protocol",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy:    {Value: tnsapi.ManagedByValue},
				tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-123"},
			},
			want: false,
		},
		{
			name: "NFS volume missing share path",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
				tnsapi.PropertyProtocol:  {Value: tnsapi.ProtocolNFS},
			},
			want: false,
		},
		{
			name: "NVMe-oF volume missing NQN",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
				tnsapi.PropertyProtocol:  {Value: tnsapi.ProtocolNVMeOF},
			},
			want: false,
		},
		{
			name: "unknown protocol",
			props: map[string]tnsapi.UserProperty{
				tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
				tnsapi.PropertyProtocol:  {Value: "iscsi"},
			},
			want: false,
		},
		{
			name:  "empty properties",
			props: map[string]tnsapi.UserProperty{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsVolumeAdoptable(tt.props)
			if got != tt.want {
				t.Errorf("IsVolumeAdoptable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGetAdoptionInfo tests the GetAdoptionInfo function.
func TestGetAdoptionInfo(t *testing.T) {
	props := map[string]tnsapi.UserProperty{
		tnsapi.PropertyCSIVolumeName:    {Value: "pvc-12345678"},
		tnsapi.PropertyProtocol:         {Value: tnsapi.ProtocolNFS},
		tnsapi.PropertyCapacityBytes:    {Value: "10737418240"},
		tnsapi.PropertyDeleteStrategy:   {Value: tnsapi.DeleteStrategyRetain},
		tnsapi.PropertyPVCName:          {Value: "my-data"},
		tnsapi.PropertyPVCNamespace:     {Value: "production"},
		tnsapi.PropertyStorageClass:     {Value: "truenas-nfs"},
		tnsapi.PropertyNFSSharePath:     {Value: "/mnt/tank/csi/pvc-12345678"},
		tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.test"}, // Should be extracted even for NFS
	}

	info := GetAdoptionInfo(props)

	expectedFields := map[string]string{
		"volumeID":       "pvc-12345678",
		"protocol":       "nfs",
		"capacityBytes":  "10737418240",
		"deleteStrategy": "retain",
		"pvcName":        "my-data",
		"pvcNamespace":   "production",
		"storageClass":   "truenas-nfs",
		"nfsSharePath":   "/mnt/tank/csi/pvc-12345678",
		"nvmeofNQN":      "nqn.test",
	}

	for key, want := range expectedFields {
		if got := info[key]; got != want {
			t.Errorf("GetAdoptionInfo()[%s] = %q, want %q", key, got, want)
		}
	}
}

// TestGetAdoptionInfo_Partial tests GetAdoptionInfo with partial properties.
func TestGetAdoptionInfo_Partial(t *testing.T) {
	// Minimal properties - only protocol and volume name
	props := map[string]tnsapi.UserProperty{
		tnsapi.PropertyCSIVolumeName: {Value: "minimal-volume"},
		tnsapi.PropertyProtocol:      {Value: tnsapi.ProtocolNVMeOF},
	}

	info := GetAdoptionInfo(props)

	// These should be present
	if info["volumeID"] != "minimal-volume" {
		t.Errorf("Expected volumeID to be 'minimal-volume', got %q", info["volumeID"])
	}
	if info["protocol"] != "nvmeof" {
		t.Errorf("Expected protocol to be 'nvmeof', got %q", info["protocol"])
	}

	// These should be empty (not set)
	if info["pvcName"] != "" {
		t.Errorf("Expected pvcName to be empty, got %q", info["pvcName"])
	}
	if info["storageClass"] != "" {
		t.Errorf("Expected storageClass to be empty, got %q", info["storageClass"])
	}
}

// TestCheckAndAdoptVolume_AdoptableVolumeFound tests the checkAndAdoptVolume function
// when an adoptable volume is found and adoption is allowed.
func TestCheckAndAdoptVolume_AdoptableVolumeFound(t *testing.T) {
	ctx := context.Background()
	mockClient := &MockAPIClientForSnapshots{
		// Volume is found by CSI name
		FindDatasetByCSIVolumeNameFunc: func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
			if volumeName == "pvc-adoptable" {
				return &tnsapi.DatasetWithProperties{
					Dataset: tnsapi.Dataset{
						ID:         "tank/csi/pvc-adoptable",
						Name:       "tank/csi/pvc-adoptable",
						Mountpoint: "/mnt/tank/csi/pvc-adoptable",
					},
					UserProperties: map[string]tnsapi.UserProperty{
						tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
						tnsapi.PropertySchemaVersion:  {Value: tnsapi.SchemaVersionV1},
						tnsapi.PropertyProtocol:       {Value: tnsapi.ProtocolNFS},
						tnsapi.PropertyAdoptable:      {Value: "true"},
						tnsapi.PropertyNFSSharePath:   {Value: "/mnt/tank/csi/pvc-adoptable"},
						tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
						tnsapi.PropertyCSIVolumeName:  {Value: "pvc-adoptable"},
						tnsapi.PropertyDeleteStrategy: {Value: tnsapi.DeleteStrategyDelete},
					},
				}, nil
			}
			return nil, nil //nolint:nilnil // intentional: volume not found
		},
		// NFS share query - no existing share
		QueryNFSShareFunc: func(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
			return []tnsapi.NFSShare{}, nil
		},
		// NFS share creation
		CreateNFSShareFunc: func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
			return &tnsapi.NFSShare{
				ID:      99,
				Path:    params.Path,
				Enabled: true,
			}, nil
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry())
	req := &csi.CreateVolumeRequest{
		Name: "pvc-adoptable",
		Parameters: map[string]string{
			"protocol": "nfs",
			"server":   "192.168.1.100",
			"pool":     "tank",
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824, // 1GB
		},
	}
	params := req.GetParameters()

	resp, adopted, err := service.checkAndAdoptVolume(ctx, req, params, "nfs")

	if err != nil {
		t.Fatalf("checkAndAdoptVolume() unexpected error: %v", err)
	}
	if !adopted {
		t.Fatal("checkAndAdoptVolume() expected adopted=true")
	}
	if resp == nil {
		t.Fatal("checkAndAdoptVolume() expected non-nil response")
	}
	if resp.Volume.VolumeId != "pvc-adoptable" {
		t.Errorf("Expected volume ID 'pvc-adoptable', got '%s'", resp.Volume.VolumeId)
	}
}

// TestCheckAndAdoptVolume_NotAdoptable tests the checkAndAdoptVolume function
// when a volume is found but not marked as adoptable and adoptExisting is not set.
func TestCheckAndAdoptVolume_NotAdoptable(t *testing.T) {
	ctx := context.Background()
	mockClient := &MockAPIClientForSnapshots{
		// Volume is found by CSI name but not marked adoptable
		FindDatasetByCSIVolumeNameFunc: func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
			if volumeName == "pvc-not-adoptable" {
				return &tnsapi.DatasetWithProperties{
					Dataset: tnsapi.Dataset{
						ID:         "tank/csi/pvc-not-adoptable",
						Name:       "tank/csi/pvc-not-adoptable",
						Mountpoint: "/mnt/tank/csi/pvc-not-adoptable",
					},
					UserProperties: map[string]tnsapi.UserProperty{
						tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
						tnsapi.PropertySchemaVersion:  {Value: tnsapi.SchemaVersionV1},
						tnsapi.PropertyProtocol:       {Value: tnsapi.ProtocolNFS},
						tnsapi.PropertyNFSSharePath:   {Value: "/mnt/tank/csi/pvc-not-adoptable"},
						tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
						tnsapi.PropertyCSIVolumeName:  {Value: "pvc-not-adoptable"},
						tnsapi.PropertyDeleteStrategy: {Value: tnsapi.DeleteStrategyDelete},
						// No PropertyAdoptable set
					},
				}, nil
			}
			return nil, nil //nolint:nilnil // intentional: volume not found
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry())
	req := &csi.CreateVolumeRequest{
		Name: "pvc-not-adoptable",
		Parameters: map[string]string{
			"protocol": "nfs",
			"server":   "192.168.1.100",
			"pool":     "tank",
			// No adoptExisting parameter
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824,
		},
	}
	params := req.GetParameters()

	resp, adopted, err := service.checkAndAdoptVolume(ctx, req, params, "nfs")

	if err != nil {
		t.Fatalf("checkAndAdoptVolume() unexpected error: %v", err)
	}
	if adopted {
		t.Fatal("checkAndAdoptVolume() expected adopted=false (volume not adoptable)")
	}
	if resp != nil {
		t.Fatal("checkAndAdoptVolume() expected nil response when not adopted")
	}
}

// TestCheckAndAdoptVolume_AdoptExisting tests the checkAndAdoptVolume function
// when adoptExisting=true is set in StorageClass, allowing adoption of any managed volume.
func TestCheckAndAdoptVolume_AdoptExisting(t *testing.T) {
	ctx := context.Background()
	mockClient := &MockAPIClientForSnapshots{
		// Volume is found by CSI name (not marked adoptable, but adoptExisting=true)
		FindDatasetByCSIVolumeNameFunc: func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
			if volumeName == "pvc-adopt-existing" {
				return &tnsapi.DatasetWithProperties{
					Dataset: tnsapi.Dataset{
						ID:         "tank/csi/pvc-adopt-existing",
						Name:       "tank/csi/pvc-adopt-existing",
						Mountpoint: "/mnt/tank/csi/pvc-adopt-existing",
					},
					UserProperties: map[string]tnsapi.UserProperty{
						tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
						tnsapi.PropertySchemaVersion:  {Value: tnsapi.SchemaVersionV1},
						tnsapi.PropertyProtocol:       {Value: tnsapi.ProtocolNFS},
						tnsapi.PropertyNFSSharePath:   {Value: "/mnt/tank/csi/pvc-adopt-existing"},
						tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
						tnsapi.PropertyCSIVolumeName:  {Value: "pvc-adopt-existing"},
						tnsapi.PropertyDeleteStrategy: {Value: tnsapi.DeleteStrategyDelete},
						// No PropertyAdoptable - but adoptExisting will be true
					},
				}, nil
			}
			return nil, nil //nolint:nilnil // intentional: volume not found
		},
		QueryNFSShareFunc: func(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
			// Existing share found
			return []tnsapi.NFSShare{
				{ID: 88, Path: path, Enabled: true},
			}, nil
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry())
	req := &csi.CreateVolumeRequest{
		Name: "pvc-adopt-existing",
		Parameters: map[string]string{
			"protocol":      "nfs",
			"server":        "192.168.1.100",
			"pool":          "tank",
			"adoptExisting": "true", // This allows adoption even without adoptable=true
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824,
		},
	}
	params := req.GetParameters()

	resp, adopted, err := service.checkAndAdoptVolume(ctx, req, params, "nfs")

	if err != nil {
		t.Fatalf("checkAndAdoptVolume() unexpected error: %v", err)
	}
	if !adopted {
		t.Fatal("checkAndAdoptVolume() expected adopted=true (adoptExisting=true)")
	}
	if resp == nil {
		t.Fatal("checkAndAdoptVolume() expected non-nil response")
	}
}

// TestCheckAndAdoptVolume_ProtocolMismatch tests that adoption fails when
// the volume's protocol doesn't match the requested protocol.
func TestCheckAndAdoptVolume_ProtocolMismatch(t *testing.T) {
	ctx := context.Background()
	mockClient := &MockAPIClientForSnapshots{
		// NFS volume found, but NVMe-oF protocol requested
		FindDatasetByCSIVolumeNameFunc: func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
			if volumeName == "pvc-protocol-mismatch" {
				return &tnsapi.DatasetWithProperties{
					Dataset: tnsapi.Dataset{
						ID:   "tank/csi/pvc-protocol-mismatch",
						Name: "tank/csi/pvc-protocol-mismatch",
					},
					UserProperties: map[string]tnsapi.UserProperty{
						tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
						tnsapi.PropertySchemaVersion:  {Value: tnsapi.SchemaVersionV1},
						tnsapi.PropertyProtocol:       {Value: tnsapi.ProtocolNFS}, // Volume is NFS
						tnsapi.PropertyAdoptable:      {Value: "true"},
						tnsapi.PropertyNFSSharePath:   {Value: "/mnt/tank/csi/pvc-protocol-mismatch"},
						tnsapi.PropertyCSIVolumeName:  {Value: "pvc-protocol-mismatch"},
						tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
						tnsapi.PropertyDeleteStrategy: {Value: tnsapi.DeleteStrategyDelete},
					},
				}, nil
			}
			return nil, nil //nolint:nilnil // intentional: volume not found
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry())
	req := &csi.CreateVolumeRequest{
		Name: "pvc-protocol-mismatch",
		Parameters: map[string]string{
			"protocol": "nvmeof", // Requested NVMe-oF but volume is NFS
			"server":   "192.168.1.100",
			"pool":     "tank",
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824,
		},
	}
	params := req.GetParameters()

	resp, adopted, err := service.checkAndAdoptVolume(ctx, req, params, "nvmeof")

	// Should fail with protocol mismatch error
	if err == nil {
		t.Fatal("checkAndAdoptVolume() expected error for protocol mismatch")
	}
	if !adopted {
		t.Fatal("checkAndAdoptVolume() expected adopted=true (indicates we tried to adopt)")
	}
	if resp != nil {
		t.Fatal("checkAndAdoptVolume() expected nil response on error")
	}
}

// TestCheckAndAdoptVolume_NoVolumeFound tests that checkAndAdoptVolume returns
// adopted=false when no volume is found.
func TestCheckAndAdoptVolume_NoVolumeFound(t *testing.T) {
	ctx := context.Background()
	mockClient := &MockAPIClientForSnapshots{
		FindDatasetByCSIVolumeNameFunc: func(ctx context.Context, prefix, volumeName string) (*tnsapi.DatasetWithProperties, error) {
			return nil, nil //nolint:nilnil // No volume found
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry())
	req := &csi.CreateVolumeRequest{
		Name: "pvc-new",
		Parameters: map[string]string{
			"protocol": "nfs",
			"server":   "192.168.1.100",
			"pool":     "tank",
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824,
		},
	}
	params := req.GetParameters()

	resp, adopted, err := service.checkAndAdoptVolume(ctx, req, params, "nfs")

	if err != nil {
		t.Fatalf("checkAndAdoptVolume() unexpected error: %v", err)
	}
	if adopted {
		t.Fatal("checkAndAdoptVolume() expected adopted=false (no volume found)")
	}
	if resp != nil {
		t.Fatal("checkAndAdoptVolume() expected nil response when no volume found")
	}
}
