package driver

import (
	"context"
	"errors"
	"strings"
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
			name: "successful NVMe-oF volume creation with independent subsystem",
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
					// Note: subsystemNQN is NO LONGER required - generated automatically
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
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					expectedNQN := "nqn.2137.csi.tns:test-nvmeof-volume"
					if params.Name != expectedNQN {
						t.Errorf("Expected NQN %s, got %s", expectedNQN, params.Name)
					}
					if !params.AllowAnyHost {
						t.Error("Expected AllowAnyHost to be true")
					}
					return &tnsapi.NVMeOFSubsystem{
						ID:   100,
						Name: expectedNQN,
						NQN:  expectedNQN,
					}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", subsystemID)
					}
					if portID != 1 {
						t.Errorf("Expected port ID 1, got %d", portID)
					}
					return nil
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
					if params.NSID != 1 {
						t.Errorf("Expected NSID 1 for independent subsystem, got %d", params.NSID)
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
				expectedNQN := "nqn.2137.csi.tns:test-nvmeof-volume"
				if resp.Volume.VolumeContext["nqn"] != expectedNQN {
					t.Errorf("Expected NQN %s, got %s", expectedNQN, resp.Volume.VolumeContext["nqn"])
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
				// NSID is always 1 with independent subsystem architecture
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
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
				// No capacity specified - should default to 1GB
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
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
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					return nil
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
					"protocol": "nvmeof",
					"server":   "192.168.1.100",
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
					"protocol": "nvmeof",
					"pool":     "tank",
					// Missing server parameter
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "no NVMe-oF ports configured",
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
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume",
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					// No ports configured
					return []tnsapi.NVMeOFPort{}, nil
				}
				// Cleanup mocks
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
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
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("insufficient space in pool")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "subsystem creation failure with cleanup",
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
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				zvolCreated := false
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					zvolCreated = true
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume",
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return nil, errors.New("failed to create subsystem")
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
		{
			name: "namespace creation failure with full cleanup",
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
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				zvolCreated := false
				subsystemCreated := false
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					zvolCreated = true
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume",
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					subsystemCreated = true
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					return nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					return nil, errors.New("failed to create namespace")
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					if !subsystemCreated {
						t.Error("DeleteNVMeOFSubsystem called before CreateNVMeOFSubsystem")
					}
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", subsystemID)
					}
					return nil
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
		{
			name: "port binding failure with cleanup",
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
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					return []tnsapi.Dataset{}, nil
				}
				m.CreateZvolFunc = func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:   "tank/test-nvmeof-volume",
						Name: "tank/test-nvmeof-volume",
						Type: "VOLUME",
					}, nil
				}
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					return errors.New("failed to bind subsystem to port")
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
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
			name: "successful NVMe-oF volume deletion (namespace, subsystem, and ZVOL)",
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
				subsystemDeleted := false
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					if namespaceID != 200 {
						t.Errorf("Expected namespace ID 200, got %d", namespaceID)
					}
					namespaceDeleted = true
					return nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					// Namespace is deleted
					return []tnsapi.NVMeOFNamespace{}, nil
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					if !namespaceDeleted {
						t.Error("Expected namespace to be deleted before subsystem")
					}
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", subsystemID)
					}
					subsystemDeleted = true
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if !subsystemDeleted {
						t.Error("Expected subsystem to be deleted before ZVOL")
					}
					if datasetID != "tank/test-nvmeof-volume" {
						t.Errorf("Expected dataset ID tank/test-nvmeof-volume, got %s", datasetID)
					}
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
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should succeed due to idempotency
		},
		{
			name: "idempotent deletion - subsystem not found",
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
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return errors.New("subsystem not found")
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
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
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
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should still delete subsystem and ZVOL
		},
		{
			name: "deletion with missing subsystem ID",
			meta: &VolumeMetadata{
				Name:              "test-nvmeof-volume",
				Protocol:          ProtocolNVMeOF,
				DatasetID:         "tank/test-nvmeof-volume",
				DatasetName:       "tank/test-nvmeof-volume",
				NVMeOFSubsystemID: 0, // Missing subsystem ID
				NVMeOFNamespaceID: 200,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNVMeOFNamespaceFunc = func(ctx context.Context, namespaceID int) error {
					return nil
				}
				m.QueryAllNVMeOFNamespacesFunc = func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
					return []tnsapi.NVMeOFNamespace{}, nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should still delete namespace and ZVOL
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
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful NVMe-oF volume setup from clone with independent subsystem",
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
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
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
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				expectedNQN := "nqn.2137.csi.tns:cloned-nvmeof-volume"
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					if params.Name != expectedNQN {
						t.Errorf("Expected NQN %s, got %s", expectedNQN, params.Name)
					}
					if !params.AllowAnyHost {
						t.Error("Expected AllowAnyHost to be true")
					}
					return &tnsapi.NVMeOFSubsystem{
						ID:   100,
						Name: expectedNQN,
						NQN:  expectedNQN,
					}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", subsystemID)
					}
					return nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					if params.SubsysID != 100 {
						t.Errorf("Expected subsystem ID 100, got %d", params.SubsysID)
					}
					if params.DevicePath != "zvol/tank/cloned-nvmeof-volume" {
						t.Errorf("Expected device path zvol/tank/cloned-nvmeof-volume, got %s", params.DevicePath)
					}
					if params.NSID != 1 {
						t.Errorf("Expected NSID 1 for independent subsystem, got %d", params.NSID)
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
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				expectedNQN := "nqn.2137.csi.tns:cloned-nvmeof-volume"
				if resp.Volume.VolumeContext["nqn"] != expectedNQN {
					t.Errorf("Expected NQN %s, got %s", expectedNQN, resp.Volume.VolumeContext["nqn"])
				}
				if resp.Volume.VolumeContext["datasetName"] != "tank/cloned-nvmeof-volume" {
					t.Errorf("Expected dataset name, got %s", resp.Volume.VolumeContext["datasetName"])
				}
				if resp.Volume.VolumeContext["nsid"] != "1" {
					t.Errorf("Expected NSID 1, got %s", resp.Volume.VolumeContext["nsid"])
				}
				if resp.Volume.VolumeContext["clonedFromSnapshot"] != "true" {
					t.Error("Expected clonedFromSnapshot to be true")
				}
				if resp.Volume.ContentSource == nil {
					t.Error("Expected ContentSource to be non-nil for cloned volume")
				}
			},
		},
		{
			name: "subsystem creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return nil, errors.New("subsystem creation failed")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/cloned-nvmeof-volume" {
						t.Errorf("Expected cleanup of dataset tank/cloned-nvmeof-volume, got %s", datasetID)
					}
					return nil
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "port binding failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					return errors.New("port binding failed")
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100 for cleanup, got %d", subsystemID)
					}
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/cloned-nvmeof-volume" {
						t.Errorf("Expected cleanup of dataset tank/cloned-nvmeof-volume, got %s", datasetID)
					}
					return nil
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "namespace creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nvmeof-volume",
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			zvol: &tnsapi.Dataset{
				ID:   "tank/cloned-nvmeof-volume",
				Name: "tank/cloned-nvmeof-volume",
				Type: "VOLUME",
			},
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateNVMeOFSubsystemFunc = func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
					return &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: params.Name}, nil
				}
				m.QueryNVMeOFPortsFunc = func(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
					return []tnsapi.NVMeOFPort{{ID: 1}}, nil
				}
				m.AddSubsystemToPortFunc = func(ctx context.Context, subsystemID, portID int) error {
					return nil
				}
				m.CreateNVMeOFNamespaceFunc = func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
					return nil, errors.New("failed to create namespace")
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, subsystemID int) error {
					if subsystemID != 100 {
						t.Errorf("Expected subsystem ID 100 for cleanup, got %d", subsystemID)
					}
					return nil
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
			// Note: subsystemNQN parameter is ignored in the new architecture - NQN is generated from volume name
			testCloneInfo := &cloneInfo{
				Mode:       "cow",
				SnapshotID: "snapshot-id",
			}
			resp, err := controller.setupNVMeOFVolumeFromClone(ctx, tt.req, tt.zvol, tt.server, "", testCloneInfo)

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

func TestGenerateNQN(t *testing.T) {
	tests := []string{
		"pvc-12345",
		"my-volume",
		"test-nvmeof-volume",
	}

	for _, volumeName := range tests {
		t.Run(volumeName, func(t *testing.T) {
			result := generateNQN(defaultNQNPrefix, volumeName)

			parts := strings.Split(result, ":")
			if len(parts) != 2 {
				t.Fatalf("generateNQN(%s) = %s, expected 2 colon-separated parts", volumeName, result)
			}

			expectedPrefix := defaultNQNPrefix + ":"
			if !strings.HasPrefix(result, expectedPrefix) {
				t.Fatalf("generateNQN(%s) = %s, expected prefix %s", volumeName, result, expectedPrefix)
			}

			if parts[1] != volumeName {
				t.Fatalf("generateNQN(%s) = %s, expected suffix volume name %s", volumeName, result, volumeName)
			}
		})
	}
}
