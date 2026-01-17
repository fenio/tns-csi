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

func TestCreateNFSVolume(t *testing.T) {
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
			name: "successful NFS volume creation",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
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
					RequiredBytes: 1 * 1024 * 1024 * 1024, // 1GB
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					// No existing datasets - allow creation
					return []tnsapi.Dataset{}, nil
				}
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/csi/test-nfs-volume",
						Name:       "tank/csi/test-nfs-volume",
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/csi/test-nfs-volume",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return &tnsapi.NFSShare{
						ID:      1,
						Path:    "/mnt/tank/csi/test-nfs-volume",
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
				if resp.Volume.CapacityBytes != 1*1024*1024*1024 {
					t.Errorf("Expected capacity 1GB, got %d", resp.Volume.CapacityBytes)
				}
				// Check volume context
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				if resp.Volume.VolumeContext["share"] != "/mnt/tank/csi/test-nfs-volume" {
					t.Errorf("Expected share path, got %s", resp.Volume.VolumeContext["share"])
				}
			},
		},
		{
			name: "NFS volume creation with default capacity",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume-default",
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
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
				// No capacity specified - should default to 1GB
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.QueryAllDatasetsFunc = func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
					// No existing datasets - allow creation
					return []tnsapi.Dataset{}, nil
				}
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					return &tnsapi.Dataset{
						ID:         "tank/test-nfs-volume-default",
						Name:       "tank/test-nfs-volume-default",
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/test-nfs-volume-default",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return &tnsapi.NFSShare{
						ID:      2,
						Path:    "/mnt/tank/test-nfs-volume-default",
						Enabled: true,
					}, nil
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
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"server":   "192.168.1.100",
					// Missing pool parameter
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "dataset creation failure",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("pool not found")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "NFS share creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				datasetCreated := false
				m.CreateDatasetFunc = func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
					datasetCreated = true
					return &tnsapi.Dataset{
						ID:         "tank/test-nfs-volume",
						Name:       "tank/test-nfs-volume",
						Type:       "FILESYSTEM",
						Mountpoint: "/mnt/tank/test-nfs-volume",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return nil, errors.New("NFS service not running")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if !datasetCreated {
						t.Error("DeleteDataset called before CreateDataset")
					}
					if datasetID != "tank/test-nfs-volume" {
						t.Errorf("Expected dataset ID tank/test-nfs-volume, got %s", datasetID)
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
			resp, err := controller.createNFSVolume(ctx, tt.req)

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

func TestDeleteNFSVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		name      string
		wantErr   bool
	}{
		{
			name: "successful deletion",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Both NFS share and dataset should be deleted
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					if shareID != 1 {
						t.Errorf("Expected share ID 1, got %d", shareID)
					}
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/test-nfs-volume" {
						t.Errorf("Expected dataset ID tank/test-nfs-volume, got %s", datasetID)
					}
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "idempotent deletion - dataset already deleted",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// Both share and dataset already deleted
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					return errors.New("share does not exist")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return errors.New("dataset does not exist")
				}
			},
			wantErr: false, // Should succeed due to idempotency
		},
		{
			name: "deletion with dataset error (should fail and retry)",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					return nil // Share deleted successfully
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return errors.New("some backend error")
				}
			},
			wantErr: true, // Should fail to trigger retry and prevent orphaned datasets
		},
		{
			name: "deletion with missing share ID",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  0, // Missing share ID
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				// DeleteNFSShare should NOT be called when share ID is 0
				m.DeleteNFSShareFunc = func(ctx context.Context, shareID int) error {
					t.Error("DeleteNFSShare should not be called when share ID is 0")
					return nil
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					return nil
				}
			},
			wantErr: false, // Should still delete dataset
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			tt.mockSetup(mockClient)

			controller := NewControllerService(mockClient, NewNodeRegistry())
			_, err := controller.deleteNFSVolume(ctx, tt.meta)

			if tt.wantErr && err == nil {
				t.Error("Expected error but got nil")
			} else if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestExpandNFSVolume(t *testing.T) {
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
			name: "successful NFS volume expansion",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			requiredBytes: 5 * 1024 * 1024 * 1024, // 5GB
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					if datasetID != "tank/test-nfs-volume" {
						t.Errorf("Expected dataset ID tank/test-nfs-volume, got %s", datasetID)
					}
					if params.RefQuota == nil || *params.RefQuota != 5*1024*1024*1024 {
						t.Errorf("Expected refquota 5GB, got %v", params.RefQuota)
					}
					return &tnsapi.Dataset{
						ID:   datasetID,
						Name: "tank/test-nfs-volume",
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.CapacityBytes)
				}
				if resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be false for NFS")
				}
			},
		},
		{
			name: "expansion with missing dataset ID",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "", // Missing dataset ID
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			requiredBytes: 5 * 1024 * 1024 * 1024,
			mockSetup:     func(m *MockAPIClientForSnapshots) {},
			wantErr:       true,
			wantCode:      codes.InvalidArgument,
		},
		{
			name: "TrueNAS API error during expansion",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "tank/test-nfs-volume",
				NFSShareID:  1,
			},
			requiredBytes: 5 * 1024 * 1024 * 1024,
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.UpdateDatasetFunc = func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					return nil, errors.New("dataset not found on TrueNAS")
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
			resp, err := controller.expandNFSVolume(ctx, tt.meta, tt.requiredBytes)

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

func TestSetupNFSVolumeFromClone(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req           *csi.CreateVolumeRequest
		dataset       *tnsapi.Dataset
		server        string
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful NFS volume setup from clone",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2 * 1024 * 1024 * 1024, // 2GB
				},
			},
			dataset: &tnsapi.Dataset{
				ID:         "tank/cloned-nfs-volume",
				Name:       "tank/cloned-nfs-volume",
				Type:       "FILESYSTEM",
				Mountpoint: "/mnt/tank/cloned-nfs-volume",
			},
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					if params.Path != "/mnt/tank/cloned-nfs-volume" {
						t.Errorf("Expected path /mnt/tank/cloned-nfs-volume, got %s", params.Path)
					}
					return &tnsapi.NFSShare{
						ID:      10,
						Path:    params.Path,
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
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
				if resp.Volume.VolumeContext["share"] != "/mnt/tank/cloned-nfs-volume" {
					t.Errorf("Expected share path, got %s", resp.Volume.VolumeContext["share"])
				}
			},
		},
		{
			name: "NFS share creation failure with cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "cloned-nfs-volume",
			},
			dataset: &tnsapi.Dataset{
				ID:         "tank/cloned-nfs-volume",
				Name:       "tank/cloned-nfs-volume",
				Type:       "FILESYSTEM",
				Mountpoint: "/mnt/tank/cloned-nfs-volume",
			},
			server: "192.168.1.100",
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.CreateNFSShareFunc = func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return nil, errors.New("NFS service not available")
				}
				m.DeleteDatasetFunc = func(ctx context.Context, datasetID string) error {
					if datasetID != "tank/cloned-nfs-volume" {
						t.Errorf("Expected cleanup of dataset tank/cloned-nfs-volume, got %s", datasetID)
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
			resp, err := controller.setupNFSVolumeFromClone(ctx, tt.req, tt.dataset, tt.server, "snapshot-id")

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

func TestParseEncryptionConfig(t *testing.T) {
	//nolint:govet // fieldalignment: test struct layout prioritizes readability
	tests := []struct {
		name     string
		params   map[string]string
		secrets  map[string]string
		expected *encryptionConfig
	}{
		{
			name: "encryption disabled (default)",
			params: map[string]string{
				"protocol": "nfs",
				"pool":     "tank",
			},
			secrets:  nil,
			expected: nil,
		},
		{
			name: "encryption disabled explicitly",
			params: map[string]string{
				"encryption": "false",
			},
			secrets:  nil,
			expected: nil,
		},
		{
			name: "encryption enabled with auto-generate key",
			params: map[string]string{
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			},
			secrets: nil,
			expected: &encryptionConfig{
				Enabled:     true,
				Algorithm:   "AES-256-GCM", // default
				GenerateKey: true,
			},
		},
		{
			name: "encryption enabled with custom algorithm",
			params: map[string]string{
				"encryption":            "true",
				"encryptionAlgorithm":   "AES-128-CCM",
				"encryptionGenerateKey": "true",
			},
			secrets: nil,
			expected: &encryptionConfig{
				Enabled:     true,
				Algorithm:   "AES-128-CCM",
				GenerateKey: true,
			},
		},
		{
			name: "encryption with passphrase from secret",
			params: map[string]string{
				"encryption": "true",
			},
			secrets: map[string]string{
				"encryptionPassphrase": "mysecretpassphrase",
			},
			expected: &encryptionConfig{
				Enabled:    true,
				Algorithm:  "AES-256-GCM",
				Passphrase: "mysecretpassphrase",
			},
		},
		{
			name: "encryption with hex key from secret",
			params: map[string]string{
				"encryption": "true",
			},
			secrets: map[string]string{
				"encryptionKey": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
			expected: &encryptionConfig{
				Enabled:   true,
				Algorithm: "AES-256-GCM",
				Key:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
		{
			name: "encryption enabled but no key source (warning case)",
			params: map[string]string{
				"encryption": "true",
			},
			secrets: nil,
			expected: &encryptionConfig{
				Enabled:   true,
				Algorithm: "AES-256-GCM",
			},
		},
		{
			name: "encryption with mixed case",
			params: map[string]string{
				"encryption":            "TRUE",
				"encryptionGenerateKey": "True",
			},
			secrets: nil,
			expected: &encryptionConfig{
				Enabled:     true,
				Algorithm:   "AES-256-GCM",
				GenerateKey: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseEncryptionConfig(tt.params, tt.secrets)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("Expected nil, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Errorf("Expected %+v, got nil", tt.expected)
				return
			}

			if result.Enabled != tt.expected.Enabled {
				t.Errorf("Enabled: expected %v, got %v", tt.expected.Enabled, result.Enabled)
			}
			if result.Algorithm != tt.expected.Algorithm {
				t.Errorf("Algorithm: expected %v, got %v", tt.expected.Algorithm, result.Algorithm)
			}
			if result.GenerateKey != tt.expected.GenerateKey {
				t.Errorf("GenerateKey: expected %v, got %v", tt.expected.GenerateKey, result.GenerateKey)
			}
			if result.Passphrase != tt.expected.Passphrase {
				t.Errorf("Passphrase: expected %v, got %v", tt.expected.Passphrase, result.Passphrase)
			}
			if result.Key != tt.expected.Key {
				t.Errorf("Key: expected %v, got %v", tt.expected.Key, result.Key)
			}
		})
	}
}
