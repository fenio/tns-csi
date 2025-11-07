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

// MockAPIClientForSnapshots is a mock implementation of APIClient for snapshot tests.
type MockAPIClientForSnapshots struct {
	CreateSnapshotFunc           func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error)
	DeleteSnapshotFunc           func(ctx context.Context, snapshotID string) error
	QuerySnapshotsFunc           func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error)
	CloneSnapshotFunc            func(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error)
	CreateDatasetFunc            func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	DeleteDatasetFunc            func(ctx context.Context, datasetID string) error
	GetDatasetFunc               func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error)
	UpdateDatasetFunc            func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error)
	CreateNFSShareFunc           func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShareFunc           func(ctx context.Context, shareID int) error
	QueryNFSShareFunc            func(ctx context.Context, path string) ([]tnsapi.NFSShare, error)
	CreateZvolFunc               func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)
	CreateNVMeOFSubsystemFunc    func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc    func(ctx context.Context, subsystemID int) error
	QueryNVMeOFSubsystemFunc     func(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error)
	ListAllNVMeOFSubsystemsFunc  func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error)
	CreateNVMeOFNamespaceFunc    func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	DeleteNVMeOFNamespaceFunc    func(ctx context.Context, namespaceID int) error
	QueryNVMeOFPortsFunc         func(ctx context.Context) ([]tnsapi.NVMeOFPort, error)
	AddSubsystemToPortFunc       func(ctx context.Context, subsystemID, portID int) error
	GetNVMeOFSubsystemByNQNFunc  func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error)
	QueryAllDatasetsFunc         func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error)
	QueryAllNFSSharesFunc        func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error)
	QueryAllNVMeOFNamespacesFunc func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error)
	QueryPoolFunc                func(ctx context.Context, poolName string) (*tnsapi.Pool, error)
}

func (m *MockAPIClientForSnapshots) CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, params)
	}
	return nil, errors.New("CreateSnapshotFunc not implemented")
}

func (m *MockAPIClientForSnapshots) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, snapshotID)
	}
	return errors.New("DeleteSnapshotFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QuerySnapshots(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
	if m.QuerySnapshotsFunc != nil {
		return m.QuerySnapshotsFunc(ctx, filters)
	}
	return nil, errors.New("QuerySnapshotsFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CloneSnapshot(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
	if m.CloneSnapshotFunc != nil {
		return m.CloneSnapshotFunc(ctx, params)
	}
	return nil, errors.New("CloneSnapshotFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	if m.CreateDatasetFunc != nil {
		return m.CreateDatasetFunc(ctx, params)
	}
	return nil, errors.New("CreateDatasetFunc not implemented")
}

func (m *MockAPIClientForSnapshots) DeleteDataset(ctx context.Context, datasetID string) error {
	if m.DeleteDatasetFunc != nil {
		return m.DeleteDatasetFunc(ctx, datasetID)
	}
	return errors.New("DeleteDatasetFunc not implemented")
}

func (m *MockAPIClientForSnapshots) GetDataset(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
	if m.GetDatasetFunc != nil {
		return m.GetDatasetFunc(ctx, datasetID)
	}
	return nil, errors.New("GetDatasetFunc not implemented")
}

func (m *MockAPIClientForSnapshots) UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
	if m.UpdateDatasetFunc != nil {
		return m.UpdateDatasetFunc(ctx, datasetID, params)
	}
	return nil, errors.New("UpdateDatasetFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	if m.CreateNFSShareFunc != nil {
		return m.CreateNFSShareFunc(ctx, params)
	}
	return nil, errors.New("CreateNFSShareFunc not implemented")
}

func (m *MockAPIClientForSnapshots) DeleteNFSShare(ctx context.Context, shareID int) error {
	if m.DeleteNFSShareFunc != nil {
		return m.DeleteNFSShareFunc(ctx, shareID)
	}
	return errors.New("DeleteNFSShareFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryNFSShare(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
	if m.QueryNFSShareFunc != nil {
		return m.QueryNFSShareFunc(ctx, path)
	}
	return nil, errors.New("QueryNFSShareFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
	if m.CreateZvolFunc != nil {
		return m.CreateZvolFunc(ctx, params)
	}
	return nil, errors.New("CreateZvolFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	if m.CreateNVMeOFSubsystemFunc != nil {
		return m.CreateNVMeOFSubsystemFunc(ctx, params)
	}
	return nil, errors.New("CreateNVMeOFSubsystemFunc not implemented")
}

func (m *MockAPIClientForSnapshots) DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error {
	if m.DeleteNVMeOFSubsystemFunc != nil {
		return m.DeleteNVMeOFSubsystemFunc(ctx, subsystemID)
	}
	return errors.New("DeleteNVMeOFSubsystemFunc not implemented")
}

func (m *MockAPIClientForSnapshots) CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
	if m.CreateNVMeOFNamespaceFunc != nil {
		return m.CreateNVMeOFNamespaceFunc(ctx, params)
	}
	return nil, errors.New("CreateNVMeOFNamespaceFunc not implemented")
}

func (m *MockAPIClientForSnapshots) DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error {
	if m.DeleteNVMeOFNamespaceFunc != nil {
		return m.DeleteNVMeOFNamespaceFunc(ctx, namespaceID)
	}
	return errors.New("DeleteNVMeOFNamespaceFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryNVMeOFPorts(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
	if m.QueryNVMeOFPortsFunc != nil {
		return m.QueryNVMeOFPortsFunc(ctx)
	}
	return nil, errors.New("QueryNVMeOFPortsFunc not implemented")
}

func (m *MockAPIClientForSnapshots) AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error {
	if m.AddSubsystemToPortFunc != nil {
		return m.AddSubsystemToPortFunc(ctx, subsystemID, portID)
	}
	return errors.New("AddSubsystemToPortFunc not implemented")
}

func (m *MockAPIClientForSnapshots) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	if m.GetNVMeOFSubsystemByNQNFunc != nil {
		return m.GetNVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	return nil, errors.New("GetNVMeOFSubsystemByNQNFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error) {
	if m.QueryNVMeOFSubsystemFunc != nil {
		return m.QueryNVMeOFSubsystemFunc(ctx, nqn)
	}
	return nil, errors.New("QueryNVMeOFSubsystemFunc not implemented")
}

func (m *MockAPIClientForSnapshots) ListAllNVMeOFSubsystems(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	if m.ListAllNVMeOFSubsystemsFunc != nil {
		return m.ListAllNVMeOFSubsystemsFunc(ctx)
	}
	return nil, errors.New("ListAllNVMeOFSubsystemsFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryAllDatasets(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
	if m.QueryAllDatasetsFunc != nil {
		return m.QueryAllDatasetsFunc(ctx, prefix)
	}
	return nil, errors.New("QueryAllDatasetsFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
	if m.QueryAllNFSSharesFunc != nil {
		return m.QueryAllNFSSharesFunc(ctx, pathPrefix)
	}
	return nil, errors.New("QueryAllNFSSharesFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryAllNVMeOFNamespaces(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
	if m.QueryAllNVMeOFNamespacesFunc != nil {
		return m.QueryAllNVMeOFNamespacesFunc(ctx)
	}
	return nil, errors.New("QueryAllNVMeOFNamespacesFunc not implemented")
}

func (m *MockAPIClientForSnapshots) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	if m.QueryPoolFunc != nil {
		return m.QueryPoolFunc(ctx, poolName)
	}
	return nil, errors.New("QueryPoolFunc not implemented")
}

func (m *MockAPIClientForSnapshots) Close() {
	// Mock client doesn't need cleanup
}

func TestEncodeDecodeSnapshotID(t *testing.T) {
	tests := []struct {
		name    string
		meta    SnapshotMetadata
		wantErr bool
	}{
		{
			name: "NFS snapshot metadata",
			meta: SnapshotMetadata{
				SnapshotName: "tank/test-volume@snap1",
				SourceVolume: "encoded-volume-id",
				DatasetName:  "tank/test-volume",
				Protocol:     "nfs",
				CreatedAt:    time.Now().Unix(),
			},
			wantErr: false,
		},
		{
			name: "NVMe-oF snapshot metadata",
			meta: SnapshotMetadata{
				SnapshotName: "tank/test-zvol@snap2",
				SourceVolume: "encoded-zvol-id",
				DatasetName:  "tank/test-zvol",
				Protocol:     "nvmeof",
				CreatedAt:    time.Now().Unix(),
			},
			wantErr: false,
		},
		{
			name: "Minimal snapshot metadata",
			meta: SnapshotMetadata{
				SnapshotName: "tank/minimal@snap",
				SourceVolume: "vol123",
				DatasetName:  "tank/minimal",
				Protocol:     "nfs",
				CreatedAt:    0,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode the metadata
			encoded, err := encodeSnapshotID(tt.meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("encodeSnapshotID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			// Verify encoded string is not empty
			if encoded == "" {
				t.Errorf("encodeSnapshotID() returned empty string")
				return
			}

			// Decode the encoded string
			decoded, err := decodeSnapshotID(encoded)
			if err != nil {
				t.Errorf("decodeSnapshotID() error = %v", err)
				return
			}

			// Verify decoded metadata matches original (except CreatedAt which is excluded from encoding)
			if decoded.SnapshotName != tt.meta.SnapshotName {
				t.Errorf("SnapshotName = %v, want %v", decoded.SnapshotName, tt.meta.SnapshotName)
			}
			if decoded.SourceVolume != tt.meta.SourceVolume {
				t.Errorf("SourceVolume = %v, want %v", decoded.SourceVolume, tt.meta.SourceVolume)
			}
			if decoded.DatasetName != tt.meta.DatasetName {
				t.Errorf("DatasetName = %v, want %v", decoded.DatasetName, tt.meta.DatasetName)
			}
			if decoded.Protocol != tt.meta.Protocol {
				t.Errorf("Protocol = %v, want %v", decoded.Protocol, tt.meta.Protocol)
			}
			// CreatedAt is intentionally excluded from encoding (json:"-" tag) to ensure deterministic snapshot IDs
			// It should always be 0 after decoding
			if decoded.CreatedAt != 0 {
				t.Errorf("CreatedAt = %v, want 0 (CreatedAt is excluded from snapshot ID encoding)", decoded.CreatedAt)
			}
		})
	}
}

func TestCreateSnapshot(t *testing.T) {
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
		req           *csi.CreateSnapshotRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateSnapshotResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful snapshot creation",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil // No existing snapshots
				}
				m.CreateSnapshotFunc = func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
					return &tnsapi.Snapshot{
						ID:      "tank/test-volume@test-snapshot",
						Dataset: "tank/test-volume",
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateSnapshotResponse) {
				t.Helper()
				if resp.Snapshot == nil {
					t.Error("Expected snapshot to be non-nil")
					return
				}
				if resp.Snapshot.SnapshotId == "" {
					t.Error("Expected snapshot ID to be non-empty")
				}
				if resp.Snapshot.SourceVolumeId != volumeID {
					t.Errorf("Expected source volume ID %s, got %s", volumeID, resp.Snapshot.SourceVolumeId)
				}
				if !resp.Snapshot.ReadyToUse {
					t.Error("Expected snapshot to be ready to use")
				}
			},
		},
		{
			name: "idempotent snapshot creation - already exists",
			req: &csi.CreateSnapshotRequest{
				Name:           "existing-snapshot",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{
							ID:      "tank/test-volume@existing-snapshot",
							Dataset: "tank/test-volume",
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateSnapshotResponse) {
				t.Helper()
				if resp.Snapshot == nil {
					t.Error("Expected snapshot to be non-nil")
					return
				}
				if resp.Snapshot.SourceVolumeId != volumeID {
					t.Errorf("Expected source volume ID %s, got %s", volumeID, resp.Snapshot.SourceVolumeId)
				}
			},
		},
		{
			name: "missing snapshot name",
			req: &csi.CreateSnapshotRequest{
				Name:           "",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing source volume ID",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: "",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid source volume ID",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: "invalid-id",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "TrueNAS API error during creation",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil
				}
				m.CreateSnapshotFunc = func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
					return nil, errors.New("TrueNAS API error")
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

			controller := NewControllerService(mockClient)
			resp, err := controller.CreateSnapshot(ctx, tt.req)

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

func TestDeleteSnapshot(t *testing.T) {
	ctx := context.Background()

	// Create a valid encoded snapshot ID for testing
	snapshotMeta := SnapshotMetadata{
		SnapshotName: "tank/test-volume@test-snapshot",
		SourceVolume: "encoded-volume-id",
		DatasetName:  "tank/test-volume",
		Protocol:     ProtocolNFS,
		CreatedAt:    time.Now().Unix(),
	}
	snapshotID, err := encodeSnapshotID(snapshotMeta)
	if err != nil {
		t.Fatalf("Failed to encode test snapshot ID: %v", err)
	}

	tests := []struct {
		req       *csi.DeleteSnapshotRequest
		mockSetup func(*MockAPIClientForSnapshots)
		name      string
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "successful snapshot deletion",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteSnapshotFunc = func(ctx context.Context, snapshotID string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "missing snapshot ID",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid snapshot ID - should succeed (idempotency)",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "invalid-id",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   false,
		},
		{
			name: "snapshot not found - should succeed (idempotency)",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteSnapshotFunc = func(ctx context.Context, snapshotID string) error {
					return errors.New("snapshot not found")
				}
			},
			wantErr: false,
		},
		{
			name: "TrueNAS API error during deletion",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteSnapshotFunc = func(ctx context.Context, snapshotID string) error {
					return errors.New("internal TrueNAS error")
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

			controller := NewControllerService(mockClient)
			_, err := controller.DeleteSnapshot(ctx, tt.req)

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

func TestListSnapshots(t *testing.T) {
	ctx := context.Background()

	// Create test volume and snapshot metadata
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

	snapshotMeta := SnapshotMetadata{
		SnapshotName: "tank/test-volume@test-snapshot",
		SourceVolume: volumeID,
		DatasetName:  "tank/test-volume",
		Protocol:     ProtocolNFS,
		CreatedAt:    time.Now().Unix(),
	}
	snapshotID, err := encodeSnapshotID(snapshotMeta)
	if err != nil {
		t.Fatalf("Failed to encode test snapshot ID: %v", err)
	}

	tests := []struct {
		req           *csi.ListSnapshotsRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.ListSnapshotsResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "list all snapshots",
			req:  &csi.ListSnapshotsRequest{},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/vol1@snap1", Dataset: "tank/vol1"},
						{ID: "tank/vol2@snap2", Dataset: "tank/vol2"},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ListSnapshotsResponse) {
				t.Helper()
				if len(resp.Entries) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "list snapshots by snapshot ID",
			req: &csi.ListSnapshotsRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/test-volume@test-snapshot", Dataset: "tank/test-volume"},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ListSnapshotsResponse) {
				t.Helper()
				if len(resp.Entries) != 1 {
					t.Errorf("Expected 1 entry, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "list snapshots by source volume ID",
			req: &csi.ListSnapshotsRequest{
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/test-volume@snap1", Dataset: "tank/test-volume"},
						{ID: "tank/test-volume@snap2", Dataset: "tank/test-volume"},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ListSnapshotsResponse) {
				t.Helper()
				if len(resp.Entries) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "invalid snapshot ID",
			req: &csi.ListSnapshotsRequest{
				SnapshotId: "invalid-id",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   false,
			checkResponse: func(t *testing.T, resp *csi.ListSnapshotsResponse) {
				t.Helper()
				if len(resp.Entries) != 0 {
					t.Errorf("Expected 0 entries for invalid snapshot ID, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "invalid source volume ID",
			req: &csi.ListSnapshotsRequest{
				SourceVolumeId: "invalid-id",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   false,
			checkResponse: func(t *testing.T, resp *csi.ListSnapshotsResponse) {
				t.Helper()
				if len(resp.Entries) != 0 {
					t.Errorf("Expected 0 entries for invalid source volume ID, got %d", len(resp.Entries))
				}
			},
		},
		{
			name: "TrueNAS API error",
			req:  &csi.ListSnapshotsRequest{},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return nil, errors.New("TrueNAS API error")
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

			controller := NewControllerService(mockClient)
			resp, err := controller.ListSnapshots(ctx, tt.req)

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

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "not found error",
			err:  errors.New("snapshot not found"),
			want: true,
		},
		{
			name: "does not exist error",
			err:  errors.New("dataset does not exist"),
			want: true,
		},
		{
			name: "ENOENT error",
			err:  errors.New("ENOENT: No such file or directory"),
			want: true,
		},
		{
			name: "other error",
			err:  errors.New("internal server error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFoundError(tt.err); got != tt.want {
				t.Errorf("isNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}
