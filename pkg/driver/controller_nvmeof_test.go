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

func TestCreateNVMeOFVolume(t *testing.T) {
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
			name: "successful NVMe-oF volume creation",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
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
					"protocol":      "nvmeof",
					"pool":          "tank",
					"server":        "192.168.1.100",
					"parentDataset": "tank/nvme",
					"subsystemNQN":  "nqn.2024-11.ai.truenas:nvme:test-subsystem",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					// No existing ZVOLs - allow creation
					return []tnsapi.Dataset{}, nil
				}
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					if nqn != "nqn.2024-11.ai.truenas:nvme:test-subsystem" {
						t.Errorf("Expected NQN nqn.2024-11.ai.truenas:nvme:test-subsystem, got %s", nqn)
					}
					return &tnsapi.NVMeOFSubsystem{
						ID:  100,
						NQN: nqn,
					}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					if params.Name != "tank/nvme/test-nvmeof-volume" {
						t.Errorf("Expected ZVOL name tank/nvme/test-nvmeof-volume, got %s", params.Name)
					}
					if params.Volsize != 10*1024*1024*1024 {
						t.Errorf("Expected volsize 10GB, got %d", params.Volsize)
					}
					return &tnsapi.Dataset{
						ID:   "tank/nvme/test-nvmeof-volume",
						Name: "tank/nvme/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					if params.SubsysID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", params.SubsysID)
					}
					if params.DevicePath != "zvol/tank/nvme/test-nvmeof-volume" {
						t.Errorf("Expected device path zvol/tank/nvme/test-nvmeof-volume, got %s", params.DevicePath)
					}
					if params.DeviceType != "ZVOL" {
						t.Errorf("Expected device type ZVOL, got %s", params.DeviceType)
					}
					return &tnsapi.NVMeOFNamespace{
						ID:   200,
						NSID: 1,
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
				if resp.Volume.CapacityBytes != 10*1024*1024*1024 {
					t.Errorf("Expected capacity 10GB, got %d", resp.Volume.CapacityBytes)
				}
				// Check volume context
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				if resp.Volume.VolumeContext["nqn"] != "nqn.2024-11.ai.truenas:nvme:test-subsystem" {
					t.Errorf("Expected NQN, got %s", resp.Volume.VolumeContext["nqn"])
				}
				if resp.Volume.VolumeContext["datasetName"] != "tank/nvme/test-nvmeof-volume" {
					t.Errorf("Expected dataset name, got %s", resp.Volume.VolumeContext["datasetName"])
				}
				if resp.Volume.VolumeContext["nvmeofSubsystemID"] != "100" {
					t.Errorf("Expected subsystem ID 100, got %s", resp.Volume.VolumeContext["nvmeofSubsystemID"])
				}
				if resp.Volume.VolumeContext["nvmeofNamespaceID"] != "200" {
					t.Errorf("Expected namespace ID 200, got %s", resp.Volume.VolumeContext["nvmeofNamespaceID"])
				}
				if resp.Volume.VolumeContext["nsid"] != "1" {
					t.Errorf("Expected NSID 1, got %s", resp.Volume.VolumeContext["nsid"])
				}
			},
		},
		{
			name: "NVMe-oF volume creation with default capacity",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume-default",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"pool":         "tank",
					"server":       "192.168.1.100",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:test-subsystem",
				},
				// No capacity specified - should default to 1GB
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					// No existing ZVOLs - allow creation
					return []tnsapi.Dataset{}, nil
				}
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, NQN: nqn}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					if params.Volsize != 1*1024*1024*1024 {
						t.Errorf("Expected default capacity 1GB, got %d", params.Volsize)
					}
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume-default",
						Name: "tank/test-nvmeof-volume-default",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					return &tnsapi.NVMeOFNamespace{ID: 200, NSID: 1}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateVolumeResponse) {
				t.Helper()
				if resp.Volume.CapacityBytes != 1*1024*1024*1024 {
					t.Errorf("Expected default capacity 1GB, got %d", resp.Volume.CapacityBytes)
				}
			},
		},
		{
			name: "missing pool parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"server":       "192.168.1.100",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:test-subsystem",
					// Missing pool parameter
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing server parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"pool":         "tank",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:test-subsystem",
					// Missing server parameter
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing subsystemNQN parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
					// Missing subsystemNQN parameter
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "subsystem not found - pre-configuration required",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"pool":         "tank",
					"server":       "192.168.1.100",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:nonexistent",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					// No existing ZVOLs - will proceed to subsystem check
					return []tnsapi.Dataset{}, nil
				}
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return nil, errors.New("subsystem not found")
				}
			},
			wantErr:  true,
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "ZVOL creation failure",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"pool":         "tank",
					"server":       "192.168.1.100",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:test-subsystem",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, NQN: nqn}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("insufficient space in pool")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "namespace creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":     "nvmeof",
					"pool":         "tank",
					"server":       "192.168.1.100",
					"subsystemNQN": "nqn.2024-11.ai.truenas:nvme:test-subsystem",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				zvolCreated := false
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, NQN: nqn}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					zvolCreated = true
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume",
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					return nil, errors.New("failed to create namespace")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if !zvolCreated {
						t.Error("DeleteDataset called before CreateZvol")
					}
					if datasetID != "tank/test-nvmeof-volume" {
						t.Errorf("Expected dataset ID tank/test-nvmeof-volume, got %s", datasetID)
					}
					return nil
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
			resp, err := controller.createNVMeOFVolume(ctx, tt.req)

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

func TestDeleteNVMeOFVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		name      string
		wantErr   bool
	}{
		{
			name: "successful NVMe-oF volume deletion (namespace and ZVOL only)",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				namespaceDeleted := false
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					if namespaceID != 200 {
						t.Errorf("Expected namespace ID 200, got %d", namespaceID)
					}
					namespaceDeleted = true
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if !namespaceDeleted {
						t.Error("Expected namespace to be deleted before ZVOL")
					}
					if datasetID != "tank/test-nvmeof-volume" {
						t.Errorf("Expected dataset ID tank/test-nvmeof-volume, got %s", datasetID)
					}
					return nil
				}
				// Subsystem should NOT be deleted
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					t.Error("Subsystem should NOT be deleted - it's pre-configured infrastructure")
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "idempotent deletion - namespace not found",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					return errors.New("namespace not found")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should succeed due to idempotency
		},
		{
			name: "idempotent deletion - ZVOL not found",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return errors.New("ZVOL does not exist")
				}
			},
			wantErr: false, // Should succeed due to idempotency
		},
		{
			name: "deletion with missing namespace ID",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 0, // Missing namespace ID
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should still delete ZVOL
		},
		{
			name: "subsystem preserved during deletion",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100, // Subsystem ID present but should NOT be deleted
				NVMeOFNamespaceID: 200,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
				// This should never be called
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					t.Error("Subsystem deletion was called but should not be - subsystems are pre-configured infrastructure")
					return nil
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			controller := NewControllerService(mockClient, NewNodeRegistry())
			_, err := controller.deleteNVMeOFVolume(ctx, tt.meta)

			if tt.wantErr && err == nil {
				t.Error("Expected error but got nil")
			} else if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestExpandNVMeOFVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.ControllerExpandVolumeResponse)
		meta          *VolumeMetadata
		name          string
		requiredBytes int64
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful NVMe-oF volume expansion",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			requiredBytes: 20 * 1024 * 1024 * 1024, // 20GB
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					if datasetID != "tank/test-nvmeof-volume" {
						t.Errorf("Expected dataset ID tank/test-nvmeof-volume, got %s", datasetID)
					}
					if params.Volsize == nil || *params.Volsize != 20*1024*1024*1024 {
						t.Errorf("Expected volsize 20GB, got %v", params.Volsize)
					}
					return &tnsapi.Dataset{
						ID:   datasetID,
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != 20*1024*1024*1024 {
					t.Errorf("Expected capacity 20GB, got %d", resp.CapacityBytes)
				}
				if !resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be true for NVMe-oF")
				}
			},
		},
		{
			name: "expansion with missing dataset ID",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "", // Missing dataset ID
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			requiredBytes: 20 * 1024 * 1024 * 1024,
			mockSetup:     func(m *MockAPIClientForSnapshots) {},
			wantErr:       true,
			wantCode:      codes.InvalidArgument,
		},
		{
			name: "TrueNAS API error during expansion",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 100,
				NVMeOFNamespaceID: 200,
			},
			requiredBytes: 20 * 1024 * 1024 * 1024,
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("ZVOL not found on TrueNAS")
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
			resp, err := controller.expandNVMeOFVolume(ctx, tt.meta, tt.requiredBytes)

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

func TestSetupNVMeOFVolumeFromClone(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req           *csi.CreateVolumeRequest
		zvol          *tnsapi.Dataset
		server        string
		subsystemNQN  string
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful NVMe-oF volume setup from clone",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024, // 5GB
				},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: "encoded-snapshot-id",
						},
					},
				},
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server:       "192.168.1.100",
			subsystemNQN: "nqn.2024-11.ai.truenas:nvme:test-subsystem",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					if nqn != "nqn.2024-11.ai.truenas:nvme:test-subsystem" {
						t.Errorf("Expected NQN nqn.2024-11.ai.truenas:nvme:test-subsystem, got %s", nqn)
					}
					return &tnsapi.NVMeOFSubsystem{
						ID:  100,
						NQN: nqn,
					}, nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					if params.SubsysID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", params.SubsysID)
					}
					if params.DevicePath != "zvol/tank/cloned-nvmeof-volume" {
						t.Errorf("Expected device path zvol/tank/cloned-nvmeof-volume, got %s", params.DevicePath)
					}
					return &tnsapi.NVMeOFNamespace{
						ID:   200,
						NSID: 2,
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
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				if resp.Volume.VolumeContext["nqn"] != "nqn.2024-11.ai.truenas:nvme:test-subsystem" {
					t.Errorf("Expected NQN, got %s", resp.Volume.VolumeContext["nqn"])
				}
				if resp.Volume.VolumeContext["datasetName"] != "tank/cloned-nvmeof-volume" {
					t.Errorf("Expected dataset name, got %s", resp.Volume.VolumeContext["datasetName"])
				}
				if resp.Volume.ContentSource == nil {
					t.Error("Expected ContentSource to be non-nil for cloned volume")
				}
			},
		},
		{
			name: "subsystem not found with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server:       "192.168.1.100",
			subsystemNQN: "nqn.2024-11.ai.truenas:nvme:nonexistent",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return nil, errors.New("subsystem not found")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/cloned-nvmeof-volume" {
						t.Errorf("Expected cleanup of dataset tank/cloned-nvmeof-volume, got %s", datasetID)
					}
					return nil
				}
			},
			wantErr:  true,
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "namespace creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server:       "192.168.1.100",
			subsystemNQN: "nqn.2024-11.ai.truenas:nvme:test-subsystem",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetNVMeOFSubsystemByNQNFunc = func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, NQN: nqn}, nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					return nil, errors.New("failed to create namespace")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/cloned-nvmeof-volume" {
						t.Errorf("Expected cleanup of cloned ZVOL tank/cloned-nvmeof-volume, got %s", datasetID)
					}
					return nil
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
			resp, err := controller.setupNVMeOFVolumeFromClone(ctx, tt.req, tt.zvol, tt.server, tt.subsystemNQN, "snapshot-id")

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
