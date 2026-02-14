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

func TestValidateISCSIParams(t *testing.T) {
	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		name     string
		req      *csi.CreateVolumeRequest
		wantErr  bool
		wantCode codes.Code
		check    func(*testing.T, *iscsiVolumeParams)
	}{
		{
			name: "valid request with all parameters",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"pool":           "tank",
					"server":         "192.168.1.100",
					"parentDataset":  "tank/csi",
					"portalId":       "1",
					"initiatorId":    "2",
					"deleteStrategy": "retain",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *iscsiVolumeParams) {
				t.Helper()
				if params.pool != "tank" {
					t.Errorf("Expected pool 'tank', got %s", params.pool)
				}
				if params.server != "192.168.1.100" {
					t.Errorf("Expected server '192.168.1.100', got %s", params.server)
				}
				if params.parentDataset != "tank/csi" {
					t.Errorf("Expected parentDataset 'tank/csi', got %s", params.parentDataset)
				}
				if params.portalID != 1 {
					t.Errorf("Expected portalID 1, got %d", params.portalID)
				}
				if params.initiatorID != 2 {
					t.Errorf("Expected initiatorID 2, got %d", params.initiatorID)
				}
				if params.deleteStrategy != "retain" {
					t.Errorf("Expected deleteStrategy 'retain', got %s", params.deleteStrategy)
				}
				if params.requestedCapacity != 10*1024*1024*1024 {
					t.Errorf("Expected capacity 10GB, got %d", params.requestedCapacity)
				}
			},
		},
		{
			name: "valid request with minimal parameters",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"pool":   "tank",
					"server": "192.168.1.100",
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *iscsiVolumeParams) {
				t.Helper()
				// parentDataset defaults to pool
				if params.parentDataset != "tank" {
					t.Errorf("Expected parentDataset to default to pool 'tank', got %s", params.parentDataset)
				}
				// deleteStrategy defaults to "delete"
				if params.deleteStrategy != "delete" {
					t.Errorf("Expected deleteStrategy to default to 'delete', got %s", params.deleteStrategy)
				}
				// Capacity defaults to 1GB
				if params.requestedCapacity != 1*1024*1024*1024 {
					t.Errorf("Expected default capacity 1GB, got %d", params.requestedCapacity)
				}
				// portalID and initiatorID default to 0 (will be resolved later)
				if params.portalID != 0 {
					t.Errorf("Expected portalID to default to 0, got %d", params.portalID)
				}
			},
		},
		{
			name: "missing pool parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"server": "192.168.1.100",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing server parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"pool": "tank",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid portalId",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"pool":     "tank",
					"server":   "192.168.1.100",
					"portalId": "invalid",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid initiatorId",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"pool":        "tank",
					"server":      "192.168.1.100",
					"initiatorId": "not-a-number",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := validateISCSIParams(tt.req)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.check != nil {
				tt.check(t, params)
			}
		})
	}
}

func TestGenerateIQN(t *testing.T) {
	tests := []struct {
		name       string
		volumeName string
		want       string
	}{
		{
			name:       "simple volume name",
			volumeName: "my-volume",
			want:       "iqn.2024-01.io.truenas.csi:my-volume",
		},
		{
			name:       "volume with special characters",
			volumeName: "pvc-abc123-def456",
			want:       "iqn.2024-01.io.truenas.csi:pvc-abc123-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateIQN(tt.volumeName)
			if got != tt.want {
				t.Errorf("generateIQN(%q) = %q, want %q", tt.volumeName, got, tt.want)
			}
		})
	}
}

func TestCreateISCSIVolume(t *testing.T) {
	ctx := context.Background()

	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		name          string
		req           *csi.CreateVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful iSCSI volume creation",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":      "iscsi",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/csi",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024, // 5GB
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   "tank/csi/test-iscsi-volume",
						Name: "tank/csi/test-iscsi-volume",
						Type: "VOLUME",
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
				if resp.Volume.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.Volume.CapacityBytes)
				}
				// Check volume context
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				if resp.Volume.VolumeContext["protocol"] != "iscsi" {
					t.Errorf("Expected protocol iscsi, got %s", resp.Volume.VolumeContext["protocol"])
				}
			},
		},
		{
			name: "idempotent creation - volume already exists with same capacity",
			req: &csi.CreateVolumeRequest{
				Name: "existing-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":      "iscsi",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/csi",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:      "tank/csi/existing-volume",
							Name:    "tank/csi/existing-volume",
							Type:    "VOLUME",
							Volsize: map[string]interface{}{"parsed": float64(5 * 1024 * 1024 * 1024)}, // 5GB
						},
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
				if resp.Volume.VolumeId != "tank/csi/existing-volume" {
					t.Errorf("Expected volume ID 'tank/csi/existing-volume', got %s", resp.Volume.VolumeId)
				}
			},
		},
		{
			name: "volume exists with different capacity - error",
			req: &csi.CreateVolumeRequest{
				Name: "existing-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":      "iscsi",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/csi",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // Requesting 10GB
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{
						{
							ID:      "tank/csi/existing-volume",
							Name:    "tank/csi/existing-volume",
							Type:    "VOLUME",
							Volsize: map[string]interface{}{"parsed": float64(5 * 1024 * 1024 * 1024)}, // Existing is 5GB
						},
					}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.AlreadyExists,
		},
		{
			name: "ZVOL creation fails",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":      "iscsi",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/csi",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("insufficient space on pool")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.createISCSIVolume(ctx, tt.req)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
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

func TestDeleteISCSIVolume(t *testing.T) {
	ctx := context.Background()

	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		name      string
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "successful deletion",
			meta: &VolumeMetadata{
				Name:          "test-volume",
				Protocol:      ProtocolISCSI,
				DatasetID:     "tank/csi/test-volume",
				ISCSITargetID: 1,
				ISCSIExtentID: 2,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "deletion with no target/extent IDs",
			meta: &VolumeMetadata{
				Name:          "orphaned-volume",
				Protocol:      ProtocolISCSI,
				DatasetID:     "tank/csi/orphaned-volume",
				ISCSITargetID: 0, // No target
				ISCSIExtentID: 0, // No extent
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "deletion fails - dataset not found (idempotent)",
			meta: &VolumeMetadata{
				Name:          "missing-volume",
				Protocol:      ProtocolISCSI,
				DatasetID:     "tank/csi/missing-volume",
				ISCSITargetID: 0,
				ISCSIExtentID: 0,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QuerySnapshotsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
					return []tnsapi.Snapshot{}, nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					// Return not found error - should be handled gracefully
					return errors.New("not found: [ENOENT]")
				}
			},
			wantErr: false, // Not found is handled gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.deleteISCSIVolume(ctx, tt.meta)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if resp == nil {
				t.Error("Expected response to be non-nil")
			}
		})
	}
}

func TestExpandISCSIVolume(t *testing.T) {
	ctx := context.Background()

	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		name          string
		meta          *VolumeMetadata
		requiredBytes int64
		mockSetup     func(*MockAPIClientForSnapshots)
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful expansion",
			meta: &VolumeMetadata{
				Name:        "test-volume",
				Protocol:    ProtocolISCSI,
				DatasetID:   "tank/csi/test-volume",
				DatasetName: "tank/csi/test-volume",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   datasetID,
						Name: datasetID,
						Type: "VOLUME",
					}, nil
				}
			},
			wantErr: false,
		},
		{
			name: "expansion fails - dataset not found",
			meta: &VolumeMetadata{
				Name:        "missing-volume",
				Protocol:    ProtocolISCSI,
				DatasetID:   "tank/csi/missing-volume",
				DatasetName: "tank/csi/missing-volume",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024,
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("dataset not found")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "expansion fails - no dataset ID",
			meta: &VolumeMetadata{
				Name:        "volume-no-dataset",
				Protocol:    ProtocolISCSI,
				DatasetID:   "", // Empty dataset ID
				DatasetName: "tank/csi/volume-no-dataset",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024,
			wantErr:       true,
			wantCode:      codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.expandISCSIVolume(ctx, tt.meta, tt.requiredBytes)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if resp == nil {
				t.Error("Expected response to be non-nil")
				return
			}
			if resp.CapacityBytes != tt.requiredBytes {
				t.Errorf("Expected capacity %d, got %d", tt.requiredBytes, resp.CapacityBytes)
			}
			// iSCSI requires node expansion
			if !resp.NodeExpansionRequired {
				t.Error("Expected NodeExpansionRequired to be true for iSCSI volumes")
			}
		})
	}
}

func TestGetISCSIVolumeInfo(t *testing.T) {
	ctx := context.Background()

	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		name      string
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		wantCode  codes.Code
		wantErr   bool
		check     func(*testing.T, *csi.ControllerGetVolumeResponse)
	}{
		{
			name: "volume exists and healthy",
			meta: &VolumeMetadata{
				Name:          "healthy-volume",
				Protocol:      ProtocolISCSI,
				DatasetID:     "tank/csi/healthy-volume",
				DatasetName:   "tank/csi/healthy-volume",
				ISCSITargetID: 10,
				ISCSIExtentID: 20,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{{
						ID:      prefix,
						Name:    prefix,
						Type:    "VOLUME",
						Volsize: map[string]interface{}{"parsed": float64(5 * 1024 * 1024 * 1024)},
					}}, nil
				}
				m.QueryISCSITargetsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITarget, error) {
					return []tnsapi.ISCSITarget{{ID: 10, Name: "healthy-volume"}}, nil
				}
				m.QueryISCSIExtentsFunc = func(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSIExtent, error) {
					return []tnsapi.ISCSIExtent{{ID: 20, Name: "healthy-volume", Enabled: true, Disk: "zvol/tank/csi/healthy-volume"}}, nil
				}
			},
			wantErr: false,
			check: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId != "healthy-volume" {
					t.Errorf("Expected volume ID 'healthy-volume', got %s", resp.Volume.VolumeId)
				}
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status to be non-nil")
					return
				}
				if resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected volume to be healthy (not abnormal)")
				}
				// Verify VolumeContext is now populated
				if resp.Volume.VolumeContext == nil {
					t.Error("Expected volume context to be non-nil")
				} else if resp.Volume.VolumeContext[VolumeContextKeyProtocol] != ProtocolISCSI {
					t.Errorf("Expected protocol 'iscsi', got %q", resp.Volume.VolumeContext[VolumeContextKeyProtocol])
				}
			},
		},
		{
			name: "volume not found",
			meta: &VolumeMetadata{
				Name:        "missing-volume",
				Protocol:    ProtocolISCSI,
				DatasetID:   "tank/csi/missing-volume",
				DatasetName: "tank/csi/missing-volume",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return nil, nil
				}
			},
			wantErr: false, // Returns abnormal status, not error
			check: func(t *testing.T, resp *csi.ControllerGetVolumeResponse) {
				t.Helper()
				if resp.Status == nil || resp.Status.VolumeCondition == nil {
					t.Error("Expected volume status to be non-nil")
					return
				}
				if !resp.Status.VolumeCondition.Abnormal {
					t.Error("Expected volume to be marked abnormal when not found")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.getISCSIVolumeInfo(ctx, tt.meta)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

func TestBuildISCSIVolumeResponse(t *testing.T) {
	volumeName := "test-volume"
	server := "192.168.1.100"
	targetIQN := "iqn.2024-01.io.truenas.csi:test-volume"
	capacity := int64(5 * 1024 * 1024 * 1024)

	zvol := &tnsapi.Dataset{
		ID:   "tank/csi/test-volume",
		Name: "tank/csi/test-volume",
		Type: "VOLUME",
	}
	target := &tnsapi.ISCSITarget{
		ID:   1,
		Name: "test-volume",
	}
	extent := &tnsapi.ISCSIExtent{
		ID:   2,
		Name: "test-volume",
	}

	resp := buildISCSIVolumeResponse(volumeName, server, targetIQN, zvol, target, extent, capacity)

	if resp == nil || resp.Volume == nil {
		t.Fatal("Expected response and volume to be non-nil")
	}

	// Check volume ID - should be the full dataset path (zvol.ID)
	if resp.Volume.VolumeId != zvol.ID {
		t.Errorf("Expected volume ID %q, got %q", zvol.ID, resp.Volume.VolumeId)
	}

	// Check capacity
	if resp.Volume.CapacityBytes != capacity {
		t.Errorf("Expected capacity %d, got %d", capacity, resp.Volume.CapacityBytes)
	}

	// Check volume context
	ctx := resp.Volume.VolumeContext
	if ctx == nil {
		t.Fatal("Expected volume context to be non-nil")
	}

	if ctx["server"] != server {
		t.Errorf("Expected server %q, got %q", server, ctx["server"])
	}
	if ctx["protocol"] != "iscsi" {
		t.Errorf("Expected protocol 'iscsi', got %q", ctx["protocol"])
	}
	if ctx[VolumeContextKeyISCSIIQN] != targetIQN {
		t.Errorf("Expected IQN %q, got %q", targetIQN, ctx[VolumeContextKeyISCSIIQN])
	}
}
