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
	CreateSnapshotFunc             func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error)
	DeleteSnapshotFunc             func(ctx context.Context, snapshotID string) error
	QuerySnapshotsFunc             func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error)
	CloneSnapshotFunc              func(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error)
	PromoteDatasetFunc             func(ctx context.Context, datasetID string) error
	CreateDatasetFunc              func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	DeleteDatasetFunc              func(ctx context.Context, datasetID string) error
	GetDatasetFunc                 func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error)
	UpdateDatasetFunc              func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error)
	CreateNFSShareFunc             func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShareFunc             func(ctx context.Context, shareID int) error
	QueryNFSShareFunc              func(ctx context.Context, path string) ([]tnsapi.NFSShare, error)
	CreateZvolFunc                 func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)
	CreateNVMeOFSubsystemFunc      func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc      func(ctx context.Context, subsystemID int) error
	QueryNVMeOFSubsystemFunc       func(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error)
	ListAllNVMeOFSubsystemsFunc    func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error)
	CreateNVMeOFNamespaceFunc      func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	DeleteNVMeOFNamespaceFunc      func(ctx context.Context, namespaceID int) error
	QueryNVMeOFPortsFunc           func(ctx context.Context) ([]tnsapi.NVMeOFPort, error)
	AddSubsystemToPortFunc         func(ctx context.Context, subsystemID, portID int) error
	NVMeOFSubsystemByNQNFunc       func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error)
	QueryAllDatasetsFunc           func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error)
	QueryAllNFSSharesFunc          func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error)
	QueryAllNVMeOFNamespacesFunc   func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error)
	QueryPoolFunc                  func(ctx context.Context, poolName string) (*tnsapi.Pool, error)
	FindDatasetByCSIVolumeNameFunc func(ctx context.Context, poolDatasetPrefix, volumeName string) (*tnsapi.DatasetWithProperties, error)
	FindDatasetsByPropertyFunc     func(ctx context.Context, poolDatasetPrefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error)
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

func (m *MockAPIClientForSnapshots) PromoteDataset(ctx context.Context, datasetID string) error {
	if m.PromoteDatasetFunc != nil {
		return m.PromoteDatasetFunc(ctx, datasetID)
	}
	// Default to success for tests that don't specifically test promotion
	return nil
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

func (m *MockAPIClientForSnapshots) Dataset(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
	if m.GetDatasetFunc != nil {
		return m.GetDatasetFunc(ctx, datasetID)
	}
	return nil, errors.New("DatasetFunc not implemented")
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

func (m *MockAPIClientForSnapshots) NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	if m.NVMeOFSubsystemByNQNFunc != nil {
		return m.NVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	return nil, errors.New("NVMeOFSubsystemByNQNFunc not implemented")
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

func (m *MockAPIClientForSnapshots) RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error {
	return nil
}

func (m *MockAPIClientForSnapshots) QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error) {
	return nil, nil
}

// ZFS User Property methods - mock implementations for Phase 1

func (m *MockAPIClientForSnapshots) SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error {
	// Mock implementation - always succeed
	return nil
}

func (m *MockAPIClientForSnapshots) SetSnapshotProperties(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error {
	// Mock implementation - always succeed
	return nil
}

func (m *MockAPIClientForSnapshots) GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error) {
	// Mock implementation - return empty map (no properties)
	return make(map[string]string), nil
}

func (m *MockAPIClientForSnapshots) GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error) {
	// Mock implementation - return empty map (no properties)
	return make(map[string]string), nil
}

func (m *MockAPIClientForSnapshots) InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error {
	// Mock implementation - always succeed
	return nil
}

func (m *MockAPIClientForSnapshots) ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error {
	// Mock implementation - always succeed
	return nil
}

// Replication methods for detached snapshots.
func (m *MockAPIClientForSnapshots) RunOnetimeReplication(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error) {
	// Mock implementation - return a job ID
	return 12345, nil
}

func (m *MockAPIClientForSnapshots) GetJobStatus(ctx context.Context, jobID int) (*tnsapi.ReplicationJobState, error) {
	// Mock implementation - return completed status
	return &tnsapi.ReplicationJobState{
		ID:       jobID,
		State:    "SUCCESS",
		Progress: map[string]interface{}{"percent": float64(100)},
	}, nil
}

func (m *MockAPIClientForSnapshots) WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error {
	// Mock implementation - always succeed immediately
	return nil
}

func (m *MockAPIClientForSnapshots) RunOnetimeReplicationAndWait(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams, pollInterval time.Duration) error {
	// Mock implementation - always succeed
	return nil
}

func (m *MockAPIClientForSnapshots) FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error) {
	if m.FindDatasetsByPropertyFunc != nil {
		return m.FindDatasetsByPropertyFunc(ctx, prefix, propertyName, propertyValue)
	}
	// Default: return empty slice (no matches)
	return nil, nil
}

func (m *MockAPIClientForSnapshots) FindManagedDatasets(ctx context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error) {
	// Mock implementation - return empty slice
	return nil, nil
}

func (m *MockAPIClientForSnapshots) FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*tnsapi.DatasetWithProperties, error) {
	if m.FindDatasetByCSIVolumeNameFunc != nil {
		return m.FindDatasetByCSIVolumeNameFunc(ctx, prefix, csiVolumeName)
	}
	// Default: return nil (not found)
	return nil, nil //nolint:nilnil // Mock returns "not found"
}

// iSCSI methods - default implementations for interface compliance.

func (m *MockAPIClientForSnapshots) GetISCSIGlobalConfig(_ context.Context) (*tnsapi.ISCSIGlobalConfig, error) {
	return &tnsapi.ISCSIGlobalConfig{
		ID:       1,
		Basename: "iqn.2005-10.org.freenas.ctl",
	}, nil
}

func (m *MockAPIClientForSnapshots) QueryISCSIPortals(_ context.Context) ([]tnsapi.ISCSIPortal, error) {
	return []tnsapi.ISCSIPortal{
		{ID: 1, Tag: 1, Listen: []tnsapi.ISCSIPortalListen{{IP: "0.0.0.0", Port: 3260}}},
	}, nil
}

func (m *MockAPIClientForSnapshots) QueryISCSIInitiators(_ context.Context) ([]tnsapi.ISCSIInitiator, error) {
	return []tnsapi.ISCSIInitiator{
		{ID: 1, Tag: 1, Initiators: []string{}},
	}, nil
}

func (m *MockAPIClientForSnapshots) CreateISCSITarget(_ context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
	return &tnsapi.ISCSITarget{
		ID:     1,
		Name:   params.Name,
		Alias:  params.Alias,
		Mode:   "ISCSI",
		Groups: params.Groups,
	}, nil
}

func (m *MockAPIClientForSnapshots) DeleteISCSITarget(_ context.Context, _ int, _ bool) error {
	return nil
}

func (m *MockAPIClientForSnapshots) QueryISCSITargets(_ context.Context, _ []interface{}) ([]tnsapi.ISCSITarget, error) {
	return []tnsapi.ISCSITarget{}, nil
}

func (m *MockAPIClientForSnapshots) ISCSITargetByName(_ context.Context, name string) (*tnsapi.ISCSITarget, error) {
	return nil, errors.New("iSCSI target not found: " + name)
}

func (m *MockAPIClientForSnapshots) CreateISCSIExtent(_ context.Context, params tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
	return &tnsapi.ISCSIExtent{
		ID:        1,
		Name:      params.Name,
		Type:      params.Type,
		Disk:      params.Disk,
		Blocksize: 512,
		Enabled:   true,
	}, nil
}

func (m *MockAPIClientForSnapshots) DeleteISCSIExtent(_ context.Context, _ int, _, _ bool) error {
	return nil
}

func (m *MockAPIClientForSnapshots) QueryISCSIExtents(_ context.Context, _ []interface{}) ([]tnsapi.ISCSIExtent, error) {
	return []tnsapi.ISCSIExtent{}, nil
}

func (m *MockAPIClientForSnapshots) ISCSIExtentByName(_ context.Context, name string) (*tnsapi.ISCSIExtent, error) {
	return nil, errors.New("iSCSI extent not found: " + name)
}

func (m *MockAPIClientForSnapshots) CreateISCSITargetExtent(_ context.Context, params tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
	return &tnsapi.ISCSITargetExtent{
		ID:     1,
		Target: params.Target,
		Extent: params.Extent,
		LunID:  params.LunID,
	}, nil
}

func (m *MockAPIClientForSnapshots) DeleteISCSITargetExtent(_ context.Context, _ int, _ bool) error {
	return nil
}

func (m *MockAPIClientForSnapshots) QueryISCSITargetExtents(_ context.Context, _ []interface{}) ([]tnsapi.ISCSITargetExtent, error) {
	return []tnsapi.ISCSITargetExtent{}, nil
}

func (m *MockAPIClientForSnapshots) ISCSITargetExtentByTarget(_ context.Context, _ int) ([]tnsapi.ISCSITargetExtent, error) {
	return []tnsapi.ISCSITargetExtent{}, nil
}

func (m *MockAPIClientForSnapshots) ReloadISCSIService(_ context.Context) error {
	return nil
}

func (m *MockAPIClientForSnapshots) Close() {
	// Mock client doesn't need cleanup
}

func TestEncodeDecodeSnapshotID(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimization not critical
	tests := []struct {
		meta             SnapshotMetadata
		name             string
		wantSnapshotName string // Expected snapshot name after decode (just the name part)
		wantErr          bool
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
			wantSnapshotName: "snap1", // Compact format only stores snapshot name
			wantErr:          false,
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
			wantSnapshotName: "snap2", // Compact format only stores snapshot name
			wantErr:          false,
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
			wantSnapshotName: "snap", // Compact format only stores snapshot name
			wantErr:          false,
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

			// Verify encoded string length is under 128 bytes (CSI spec recommendation)
			if len(encoded) > 128 {
				t.Errorf("encodeSnapshotID() returned string of %d bytes, want <= 128", len(encoded))
			}

			// Decode the encoded string
			decoded, err := decodeSnapshotID(encoded)
			if err != nil {
				t.Errorf("decodeSnapshotID() error = %v", err)
				return
			}

			// Verify decoded metadata:
			// - SnapshotName: only the snapshot name part (not full ZFS path)
			// - SourceVolume: preserved
			// - Protocol: preserved
			// - DatasetName: NOT preserved in compact format (empty string)
			if decoded.SnapshotName != tt.wantSnapshotName {
				t.Errorf("SnapshotName = %v, want %v", decoded.SnapshotName, tt.wantSnapshotName)
			}
			if decoded.SourceVolume != tt.meta.SourceVolume {
				t.Errorf("SourceVolume = %v, want %v", decoded.SourceVolume, tt.meta.SourceVolume)
			}
			// DatasetName is NOT preserved in compact format - it's resolved at runtime
			if decoded.DatasetName != "" {
				t.Errorf("DatasetName = %v, want empty (compact format doesn't store DatasetName)", decoded.DatasetName)
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

	// Use plain volume ID (CSI spec compliant - under 128 bytes)
	volumeID := "test-volume"

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
				Parameters: map[string]string{
					"protocol":      ProtocolNFS,
					"parentDataset": "tank/csi",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil // No existing snapshots
				}
				m.CreateSnapshotFunc = func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
					return &tnsapi.Snapshot{
						ID:      "tank/csi/test-volume@test-snapshot",
						Dataset: "tank/csi/test-volume",
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
				Parameters: map[string]string{
					"protocol":      ProtocolNFS,
					"parentDataset": "tank/csi",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{
							ID:      "tank/csi/test-volume@existing-snapshot",
							Dataset: "tank/csi/test-volume",
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
			name: "volume not found - no parentDataset and not in TrueNAS",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: volumeID,
				Parameters:     map[string]string{},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Return empty results - volume not found
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
			name: "TrueNAS API error during creation",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: volumeID,
				Parameters: map[string]string{
					"protocol":      ProtocolNFS,
					"parentDataset": "tank/csi",
				},
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

			controller := NewControllerService(mockClient, NewNodeRegistry())
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
	// Use volume ID that will appear in the dataset path
	volumeID := "test-volume"
	snapshotMeta := SnapshotMetadata{
		SnapshotName: "tank/" + volumeID + "@test-snapshot",
		SourceVolume: volumeID,
		DatasetName:  "tank/" + volumeID,
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
				// QuerySnapshots is called to resolve the full ZFS snapshot name
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/" + volumeID + "@test-snapshot", Dataset: "tank/" + volumeID},
					}, nil
				}
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
			name: "snapshot not found in TrueNAS - should succeed (idempotency)",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// QuerySnapshots returns empty - snapshot not found
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil
				}
			},
			wantErr: false, // Should succeed per CSI idempotency
		},
		{
			name: "TrueNAS API error during deletion",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: snapshotID,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// QuerySnapshots finds the snapshot
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/" + volumeID + "@test-snapshot", Dataset: "tank/" + volumeID},
					}, nil
				}
				// DeleteSnapshot returns a non-"not found" error
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

			controller := NewControllerService(mockClient, NewNodeRegistry())
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

	// Use plain volume ID (CSI spec compliant - under 128 bytes)
	volumeID := "test-volume"

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
				// Mock finding the NFS share for the volume
				m.QueryAllNFSSharesFunc = func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{
							ID:   42,
							Path: "/mnt/tank/csi/" + volumeID,
						},
					}, nil
				}
				// Mock finding the dataset
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:   "tank/csi/" + volumeID,
							Name: "tank/csi/" + volumeID,
						},
					}, nil
				}
				// Mock finding snapshots for this dataset
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{
						{ID: "tank/csi/test-volume@snap1", Dataset: "tank/csi/test-volume"},
						{ID: "tank/csi/test-volume@snap2", Dataset: "tank/csi/test-volume"},
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

			controller := NewControllerService(mockClient, NewNodeRegistry())
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

func TestEncodeSnapshotToken(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name   string
		offset int
		want   string
	}{
		{
			name:   "zero offset",
			offset: 0,
			want:   "0",
		},
		{
			name:   "positive offset",
			offset: 10,
			want:   "10",
		},
		{
			name:   "large offset",
			offset: 1000000,
			want:   "1000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeSnapshotToken(tt.offset)
			if got != tt.want {
				t.Errorf("encodeSnapshotToken(%d) = %v, want %v", tt.offset, got, tt.want)
			}
		})
	}
}

func TestParseSnapshotToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    int
		wantErr bool
	}{
		{
			name:    "zero offset",
			token:   "0",
			want:    0,
			wantErr: false,
		},
		{
			name:    "positive offset",
			token:   "10",
			want:    10,
			wantErr: false,
		},
		{
			name:    "large offset",
			token:   "1000000",
			want:    1000000,
			wantErr: false,
		},
		{
			name:    "invalid token - non-numeric",
			token:   "abc",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid token - empty",
			token:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid token - mixed",
			token:   "10abc",
			want:    10, // Sscanf reads partial number
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSnapshotToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSnapshotToken(%q) error = %v, wantErr %v", tt.token, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSnapshotToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestSnapshotTokenRoundtrip(t *testing.T) {
	// Test that encode -> parse gives back the original value
	offsets := []int{0, 1, 10, 100, 1000, 9999999}

	for _, offset := range offsets {
		token := encodeSnapshotToken(offset)
		parsed, err := parseSnapshotToken(token)
		if err != nil {
			t.Errorf("Failed to parse token for offset %d: %v", offset, err)
			continue
		}
		if parsed != offset {
			t.Errorf("Roundtrip failed: offset %d -> token %q -> parsed %d", offset, token, parsed)
		}
	}
}

// TestValidateCloneParameters tests the validateCloneParameters function.
func TestValidateCloneParameters(t *testing.T) {
	tests := []struct {
		name         string
		params       map[string]string
		snapshotMeta *SnapshotMetadata
		wantPool     string
		wantParent   string
		wantDataset  string
		errContains  string
		wantErr      bool
	}{
		{
			name: "pool and parentDataset provided explicitly",
			params: map[string]string{
				"pool":          "mypool",
				"parentDataset": "mypool/csi",
			},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "tank/csi/pvc-source",
				Protocol:    ProtocolNFS,
			},
			wantPool:    "mypool",
			wantParent:  "mypool/csi",
			wantDataset: "mypool/csi/test-volume",
			wantErr:     false,
		},
		{
			name:   "infer pool and parentDataset from NFS snapshot dataset",
			params: map[string]string{},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "tank/csi/pvc-source",
				Protocol:    ProtocolNFS,
			},
			wantPool:    "tank",
			wantParent:  "tank/csi",
			wantDataset: "tank/csi/test-volume",
			wantErr:     false,
		},
		{
			name:   "infer pool and parentDataset from NVMe-oF snapshot dataset",
			params: map[string]string{},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "nvmepool/zvols/pvc-source",
				Protocol:    ProtocolNVMeOF,
			},
			wantPool:    "nvmepool",
			wantParent:  "nvmepool/zvols",
			wantDataset: "nvmepool/zvols/test-volume",
			wantErr:     false,
		},
		{
			name:   "infer from pool-level dataset (no parent)",
			params: map[string]string{},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "tank/pvc-source",
				Protocol:    ProtocolNFS,
			},
			wantPool:    "tank",
			wantParent:  "tank",
			wantDataset: "tank/test-volume",
			wantErr:     false,
		},
		{
			name: "pool provided explicitly, infer parentDataset from snapshot structure",
			params: map[string]string{
				"pool": "explicitpool",
			},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "tank/csi/pvc-source",
				Protocol:    ProtocolNFS,
			},
			wantPool:    "explicitpool",
			wantParent:  "tank/csi", // Still inferred from snapshot to preserve structure
			wantDataset: "tank/csi/test-volume",
			wantErr:     false,
		},
		{
			name:   "invalid dataset name (empty)",
			params: map[string]string{},
			snapshotMeta: &SnapshotMetadata{
				DatasetName: "",
				Protocol:    ProtocolNFS,
			},
			wantErr:     true,
			errContains: "Snapshot dataset name is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock controller service
			mockClient := &MockAPIClientForSnapshots{}
			svc := &ControllerService{
				apiClient: mockClient,
			}

			// Create request
			req := &csi.CreateVolumeRequest{
				Name:       "test-volume",
				Parameters: tt.params,
			}

			// Call validateCloneParameters
			result, err := svc.validateCloneParameters(req, tt.snapshotMeta)

			// Check error
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateCloneParameters() expected error but got nil")
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("validateCloneParameters() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("validateCloneParameters() unexpected error = %v", err)
				return
			}

			// Verify results
			if result.pool != tt.wantPool {
				t.Errorf("validateCloneParameters() pool = %v, want %v", result.pool, tt.wantPool)
			}
			if result.parentDataset != tt.wantParent {
				t.Errorf("validateCloneParameters() parentDataset = %v, want %v", result.parentDataset, tt.wantParent)
			}
			if result.newDatasetName != tt.wantDataset {
				t.Errorf("validateCloneParameters() newDatasetName = %v, want %v", result.newDatasetName, tt.wantDataset)
			}
			if result.newVolumeName != "test-volume" {
				t.Errorf("validateCloneParameters() newVolumeName = %v, want %v", result.newVolumeName, "test-volume")
			}
		})
	}
}

// Helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
