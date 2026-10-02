package driver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateISCSIParams(t *testing.T) {
	tests := []struct {
		req      *csi.CreateVolumeRequest
		check    func(*testing.T, *iscsiVolumeParams)
		name     string
		wantCode codes.Code
		wantErr  bool
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

	tests := []struct {
		req           *csi.CreateVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
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
					"parentDataset": "csi",
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
					if params.Name != "tank/csi/test-iscsi-volume" {
						t.Errorf("Expected ZVOL name tank/csi/test-iscsi-volume, got %s", params.Name)
					}
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
			wantCode: codes.ResourceExhausted,
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

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		name      string
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

	tests := []struct {
		meta          *VolumeMetadata
		mockSetup     func(*MockAPIClientForSnapshots)
		name          string
		requiredBytes int64
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

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		check     func(*testing.T, *csi.ControllerGetVolumeResponse, VolumeHealth)
		name      string
		wantCode  codes.Code
		wantErr   bool
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
			check: func(t *testing.T, resp *csi.ControllerGetVolumeResponse, health VolumeHealth) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId != "healthy-volume" {
					t.Errorf("Expected volume ID 'healthy-volume', got %s", resp.Volume.VolumeId)
				}
				if health.Abnormal {
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
			check: func(t *testing.T, _ *csi.ControllerGetVolumeResponse, health VolumeHealth) {
				t.Helper()
				if !health.Abnormal {
					t.Error("Expected volume to be marked abnormal when not found")
				}
			},
		},
		{
			name: "ZVOL query failure",
			meta: &VolumeMetadata{
				Name:        "unknown-volume",
				Protocol:    ProtocolISCSI,
				DatasetName: "tank/csi/unknown-volume",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return nil, errors.New("backend unavailable")
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

			resp, health, err := controller.getISCSIVolumeInfo(ctx, tt.meta)
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
				tt.check(t, resp, health)
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

// partialISCSINAS is a stateful fake for the iSCSI objects CreateVolume builds, so a
// test can start from whatever an interrupted earlier attempt left on the NAS.
type partialISCSINAS struct {
	extent        *tnsapi.ISCSIExtent
	target        *tnsapi.ISCSITarget
	targetExtents []tnsapi.ISCSITargetExtent
	// storedExtent and storedTarget are what the ZVOL's ZFS properties point at (the
	// fallback lookup used when no target carries the volume's name).
	storedExtent *tnsapi.ISCSIExtent
	storedTarget *tnsapi.ISCSITarget
	created      []string
	deleted      []string
	failTarget   bool
}

func (n *partialISCSINAS) wire(m *MockAPIClientForSnapshots, zvol tnsapi.Dataset) {
	m.QueryAllDatasetsFunc = func(_ context.Context, _ string) ([]tnsapi.Dataset, error) {
		return []tnsapi.Dataset{zvol}, nil
	}
	m.ISCSIExtentByNameFunc = func(_ context.Context, _ string) (*tnsapi.ISCSIExtent, error) {
		return n.extent, nil
	}
	m.ISCSITargetByNameFunc = func(_ context.Context, _ string) (*tnsapi.ISCSITarget, error) {
		return n.target, nil
	}
	if n.storedTarget != nil && n.storedExtent != nil {
		m.GetDatasetPropertiesFunc = func(_ context.Context, _ string, _ []string) (map[string]string, error) {
			return map[string]string{
				tnsapi.PropertyISCSITargetID: strconv.Itoa(n.storedTarget.ID),
				tnsapi.PropertyISCSIExtentID: strconv.Itoa(n.storedExtent.ID),
				tnsapi.PropertyISCSIIQN:      "iqn.2005-10.org.freenas.ctl:" + zvol.Name[strings.LastIndex(zvol.Name, "/")+1:],
			}, nil
		}
		m.QueryISCSITargetsFunc = func(_ context.Context, _ []interface{}) ([]tnsapi.ISCSITarget, error) {
			return []tnsapi.ISCSITarget{*n.storedTarget}, nil
		}
		m.QueryISCSIExtentsFunc = func(_ context.Context, _ []interface{}) ([]tnsapi.ISCSIExtent, error) {
			return []tnsapi.ISCSIExtent{*n.storedExtent}, nil
		}
	}
	m.ISCSITargetExtentByTargetFunc = func(_ context.Context, targetID int) ([]tnsapi.ISCSITargetExtent, error) {
		var out []tnsapi.ISCSITargetExtent
		for _, te := range n.targetExtents {
			if te.Target == targetID {
				out = append(out, te)
			}
		}
		return out, nil
	}
	m.CreateISCSIExtentFunc = func(_ context.Context, p tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
		if n.extent != nil {
			return nil, errors.New("[EEXIST] iscsi_extent_create.name: Extent name must be unique")
		}
		n.created = append(n.created, "extent")
		n.extent = &tnsapi.ISCSIExtent{ID: 730, Name: p.Name, Disk: p.Disk}
		return n.extent, nil
	}
	m.CreateISCSITargetFunc = func(_ context.Context, p tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
		if n.failTarget {
			return nil, errors.New("target create failed")
		}
		if n.target != nil {
			return nil, errors.New("[EEXIST] iscsi_target_create.name: Target name already exists")
		}
		n.created = append(n.created, "target")
		n.target = &tnsapi.ISCSITarget{ID: 42, Name: p.Name}
		return n.target, nil
	}
	m.CreateISCSITargetExtentFunc = func(_ context.Context, p tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
		n.created = append(n.created, "targetextent")
		te := tnsapi.ISCSITargetExtent{ID: 7, Target: p.Target, Extent: p.Extent, LunID: p.LunID}
		n.targetExtents = append(n.targetExtents, te)
		return &te, nil
	}
	m.DeleteISCSIExtentFunc = func(_ context.Context, id int) error {
		n.deleted = append(n.deleted, fmt.Sprintf("extent/%d", id))
		return nil
	}
}

// TestCreateISCSIVolumeResumesPartialCreate covers a retry after CreateVolume was
// interrupted (e.g. the external-provisioner deadline expired) part-way through, which
// leaves some of zvol / extent / target / target-extent on the NAS. The retry must finish
// the job from whatever exists instead of panicking or failing on the duplicates.
func TestCreateISCSIVolumeResumesPartialCreate(t *testing.T) {
	const (
		volName = "pvc-2b4e450c"
		zvolID  = "tank/csi/" + volName
		ourDisk = "zvol/" + zvolID
	)
	zvol := tnsapi.Dataset{
		ID: zvolID, Name: zvolID, Type: "VOLUME",
		Volsize: map[string]interface{}{"parsed": float64(1 << 30)},
	}
	req := &csi.CreateVolumeRequest{
		Name: volName,
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
		Parameters: map[string]string{
			"protocol": "iscsi", "pool": "tank", "server": "192.168.1.100", "parentDataset": "csi",
		},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
	}

	tests := []struct {
		nas         *partialISCSINAS
		name        string
		wantCreated []string
		wantDeleted []string
		wantCode    codes.Code
	}{
		{
			name:        "zvol only",
			nas:         &partialISCSINAS{},
			wantCreated: []string{"extent", "target", "targetextent"},
		},
		{
			// The case seen in production: nil target dereferenced in handleExistingISCSIVolume.
			name:        "zvol and extent, no target",
			nas:         &partialISCSINAS{extent: &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk}},
			wantCreated: []string{"target", "targetextent"},
		},
		{
			// Previously reported success for a target with no LUN mapped.
			name: "zvol, extent and target, no target-extent",
			nas: &partialISCSINAS{
				extent: &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				target: &tnsapi.ISCSITarget{ID: 42, Name: volName},
			},
			wantCreated: []string{"targetextent"},
		},
		{
			name: "fully created",
			nas: &partialISCSINAS{
				extent:        &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				target:        &tnsapi.ISCSITarget{ID: 42, Name: volName},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 7, Target: 42, Extent: 730}},
			},
		},
		{
			// Never attach an extent that backs a different disk.
			name:     "extent name taken by another disk",
			nas:      &partialISCSINAS{extent: &tnsapi.ISCSIExtent{ID: 99, Name: volName, Disk: "zvol/tank/other"}},
			wantCode: codes.AlreadyExists,
		},
		{
			name: "target already maps a different extent",
			nas: &partialISCSINAS{
				extent:        &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				target:        &tnsapi.ISCSITarget{ID: 42, Name: volName},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 7, Target: 42, Extent: 99}},
			},
			wantCode: codes.AlreadyExists,
		},
		{
			// The node does not check the LUN, so the extent must be LUN 0.
			name: "target maps the extent at a non-zero LUN",
			nas: &partialISCSINAS{
				extent:        &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				target:        &tnsapi.ISCSITarget{ID: 42, Name: volName},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 7, Target: 42, Extent: 730, LunID: 1}},
			},
			wantCode: codes.AlreadyExists,
		},
		{
			name: "target maps the extent plus another LUN",
			nas: &partialISCSINAS{
				extent: &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				target: &tnsapi.ISCSITarget{ID: 42, Name: volName},
				targetExtents: []tnsapi.ISCSITargetExtent{
					{ID: 7, Target: 42, Extent: 730, LunID: 0},
					{ID: 8, Target: 42, Extent: 99, LunID: 1},
				},
			},
			wantCode: codes.AlreadyExists,
		},
		{
			// Target renamed (e.g. imported from another cluster): found via ZFS properties.
			name: "stored properties point at a complete volume",
			nas: &partialISCSINAS{
				storedExtent:  &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				storedTarget:  &tnsapi.ISCSITarget{ID: 42, Name: "old-" + volName},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 7, Target: 42, Extent: 730}},
			},
		},
		{
			// Stale properties must not hand out another disk; build this volume's own objects.
			name: "stored extent backs another disk",
			nas: &partialISCSINAS{
				storedExtent:  &tnsapi.ISCSIExtent{ID: 99, Name: "other", Disk: "zvol/tank/other"},
				storedTarget:  &tnsapi.ISCSITarget{ID: 50, Name: "other"},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 9, Target: 50, Extent: 99}},
			},
			wantCreated: []string{"extent", "target", "targetextent"},
		},
		{
			name: "stored target maps the extent at a non-zero LUN",
			nas: &partialISCSINAS{
				storedExtent:  &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				storedTarget:  &tnsapi.ISCSITarget{ID: 42, Name: "old-" + volName},
				targetExtents: []tnsapi.ISCSITargetExtent{{ID: 7, Target: 42, Extent: 730, LunID: 3}},
			},
			wantCode: codes.AlreadyExists,
		},
		{
			// Cleanup must not delete an extent this call did not create.
			name: "target create fails with a reused extent",
			nas: &partialISCSINAS{
				extent:     &tnsapi.ISCSIExtent{ID: 730, Name: volName, Disk: ourDisk},
				failTarget: true,
			},
			wantCode: codes.Internal,
		},
		{
			name:        "target create fails with a new extent",
			nas:         &partialISCSINAS{failTarget: true},
			wantCreated: []string{"extent"},
			wantDeleted: []string{"extent/730"},
			wantCode:    codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MockAPIClientForSnapshots{}
			tt.nas.wire(m, zvol)
			controller := &ControllerService{apiClient: m}

			resp, err := controller.createISCSIVolume(context.Background(), req)

			if tt.wantCode != codes.OK {
				if status.Code(err) != tt.wantCode {
					t.Fatalf("want code %v, got err %v", tt.wantCode, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := resp.Volume.VolumeContext[VolumeContextKeyISCSIIQN]; got != "iqn.2005-10.org.freenas.ctl:"+volName {
					t.Errorf("iqn = %q", got)
				}
				var lunsOf42 []tnsapi.ISCSITargetExtent
				for _, te := range tt.nas.targetExtents {
					if te.Target == 42 {
						lunsOf42 = append(lunsOf42, te)
					}
				}
				if len(lunsOf42) != 1 || lunsOf42[0].Extent != 730 || lunsOf42[0].LunID != 0 {
					t.Errorf("want target 42 to map only extent 730 at LUN 0, got %+v", tt.nas.targetExtents)
				}
			}
			if !slices.Equal(tt.nas.created, tt.wantCreated) {
				t.Errorf("created = %v, want %v", tt.nas.created, tt.wantCreated)
			}
			if !slices.Equal(tt.nas.deleted, tt.wantDeleted) {
				t.Errorf("deleted = %v, want %v", tt.nas.deleted, tt.wantDeleted)
			}
		})
	}
}

// TestCreateISCSIVolumeRejectsOverlappingCreate covers the provisioner retrying while the
// attempt it gave up on is still running: the overlap is refused, so a failing call can
// never clean up objects the other one has reused and returned.
func TestCreateISCSIVolumeRejectsOverlappingCreate(t *testing.T) {
	const volName = "pvc-overlap"
	req := &csi.CreateVolumeRequest{
		Name: volName,
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
		Parameters: map[string]string{
			"protocol": "iscsi", "pool": "tank", "server": "192.168.1.100", "parentDataset": "csi",
		},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	m := &MockAPIClientForSnapshots{}
	m.QueryAllDatasetsFunc = func(_ context.Context, _ string) ([]tnsapi.Dataset, error) {
		close(entered)
		<-release
		return nil, errors.New("slow NAS gave up")
	}
	controller := &ControllerService{apiClient: m}

	firstErr := make(chan error, 1)
	go func() {
		_, err := controller.createISCSIVolume(context.Background(), req)
		firstErr <- err
	}()
	<-entered

	if _, err := controller.createISCSIVolume(context.Background(), req); status.Code(err) != codes.Aborted {
		t.Fatalf("overlapping create: want Aborted, got %v", err)
	}

	close(release)
	if err := <-firstErr; status.Code(err) != codes.Internal {
		t.Fatalf("first create: want Internal, got %v", err)
	}

	// Once the first call returns, the name is free again.
	m.QueryAllDatasetsFunc = func(_ context.Context, _ string) ([]tnsapi.Dataset, error) {
		return nil, errors.New("still down")
	}
	if _, err := controller.createISCSIVolume(context.Background(), req); status.Code(err) == codes.Aborted {
		t.Fatalf("create after the first finished was still rejected as in progress: %v", err)
	}
}
