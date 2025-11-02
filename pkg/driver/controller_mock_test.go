package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockAPIClient is a mock implementation of the TrueNAS API client.
//
//nolint:dupl // Interface and mock struct have similar structure by design
type mockAPIClient struct {
	createDatasetFunc         func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	deleteDatasetFunc         func(ctx context.Context, datasetID string) error
	createNFSShareFunc        func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	deleteNFSShareFunc        func(ctx context.Context, shareID int) error
	createZvolFunc            func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)
	createNVMeOFSubsystemFunc func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	deleteNVMeOFSubsystemFunc func(ctx context.Context, subsystemID int) error
	createNVMeOFNamespaceFunc func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	deleteNVMeOFNamespaceFunc func(ctx context.Context, namespaceID int) error
}

func (m *mockAPIClient) CreateDataset(_ context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	if m.createDatasetFunc != nil {
		return m.createDatasetFunc(context.Background(), params)
	}
	return &tnsapi.Dataset{
		ID:         "test-dataset-id",
		Name:       params.Name,
		Mountpoint: "/mnt/" + params.Name,
	}, nil
}

func (m *mockAPIClient) DeleteDataset(_ context.Context, datasetID string) error {
	if m.deleteDatasetFunc != nil {
		return m.deleteDatasetFunc(context.Background(), datasetID)
	}
	return nil
}

func (m *mockAPIClient) CreateNFSShare(_ context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	if m.createNFSShareFunc != nil {
		return m.createNFSShareFunc(context.Background(), params)
	}
	return &tnsapi.NFSShare{
		ID:      123,
		Path:    params.Path,
		Enabled: params.Enabled,
	}, nil
}

func (m *mockAPIClient) DeleteNFSShare(_ context.Context, shareID int) error {
	if m.deleteNFSShareFunc != nil {
		return m.deleteNFSShareFunc(context.Background(), shareID)
	}
	return nil
}

func (m *mockAPIClient) CreateZvol(_ context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
	if m.createZvolFunc != nil {
		return m.createZvolFunc(context.Background(), params)
	}
	return &tnsapi.Dataset{
		ID:   "test-zvol-id",
		Name: params.Name,
		Type: "VOLUME",
	}, nil
}

func (m *mockAPIClient) CreateNVMeOFSubsystem(_ context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	if m.createNVMeOFSubsystemFunc != nil {
		return m.createNVMeOFSubsystemFunc(context.Background(), params)
	}
	return &tnsapi.NVMeOFSubsystem{
		ID:  1,
		NQN: "nqn.2014-08.org.nvmexpress:uuid:test",
	}, nil
}

func (m *mockAPIClient) DeleteNVMeOFSubsystem(_ context.Context, subsystemID int) error {
	if m.deleteNVMeOFSubsystemFunc != nil {
		return m.deleteNVMeOFSubsystemFunc(context.Background(), subsystemID)
	}
	return nil
}

func (m *mockAPIClient) CreateNVMeOFNamespace(_ context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
	if m.createNVMeOFNamespaceFunc != nil {
		return m.createNVMeOFNamespaceFunc(context.Background(), params)
	}
	return &tnsapi.NVMeOFNamespace{
		ID:     1,
		NSID:   params.NSID,
		Device: params.DevicePath,
	}, nil
}

func (m *mockAPIClient) DeleteNVMeOFNamespace(_ context.Context, namespaceID int) error {
	if m.deleteNVMeOFNamespaceFunc != nil {
		return m.deleteNVMeOFNamespaceFunc(context.Background(), namespaceID)
	}
	return nil
}

func TestCreateVolume_Validation(t *testing.T) {
	mockClient := &mockAPIClient{}
	service := NewControllerService(mockClient)

	tests := []struct {
		name     string
		req      *csi.CreateVolumeRequest
		wantCode codes.Code
	}{
		{
			name: "Missing volume name",
			req: &csi.CreateVolumeRequest{
				Name:               "",
				VolumeCapabilities: []*csi.VolumeCapability{{}},
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Missing volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Empty volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.CreateVolume(context.Background(), tt.req)
			assert.Error(t, err)

			st, ok := status.FromError(err)
			assert.True(t, ok, "Error should be a gRPC status")
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

func TestCreateVolume_UnsupportedProtocol(t *testing.T) {
	mockClient := &mockAPIClient{}
	service := NewControllerService(mockClient)

	req := &csi.CreateVolumeRequest{
		Name: "test-volume",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
			},
		},
		Parameters: map[string]string{
			"protocol": "unsupported",
		},
	}

	_, err := service.CreateVolume(context.Background(), req)
	assert.Error(t, err)

	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "Unsupported protocol")
}

func TestDeleteVolume_Validation(t *testing.T) {
	mockClient := &mockAPIClient{}
	service := NewControllerService(mockClient)

	req := &csi.DeleteVolumeRequest{
		VolumeId: "",
	}

	_, err := service.DeleteVolume(context.Background(), req)
	assert.Error(t, err)

	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDeleteVolume_InvalidVolumeID(t *testing.T) {
	mockClient := &mockAPIClient{}
	service := NewControllerService(mockClient)

	// Test with invalid (non-encoded) volume ID
	req := &csi.DeleteVolumeRequest{
		VolumeId: "invalid-volume-id-with-slashes/",
	}

	// Should succeed (idempotent) even with invalid volume ID
	resp, err := service.DeleteVolume(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestDeleteVolume_NFSVolume(t *testing.T) {
	mockClient := &mockAPIClient{
		deleteNFSShareFunc: func(_ context.Context, shareID int) error {
			assert.Equal(t, 42, shareID)
			return nil
		},
		deleteDatasetFunc: func(_ context.Context, datasetID string) error {
			assert.Equal(t, "test-dataset-id", datasetID)
			return nil
		},
	}
	service := NewControllerService(mockClient)

	// Create a valid NFS volume ID
	meta := VolumeMetadata{
		Name:        "test-volume",
		Protocol:    "nfs",
		DatasetID:   "test-dataset-id",
		DatasetName: "tank/test-volume",
		NFSShareID:  42,
	}
	volumeID, err := encodeVolumeID(meta)
	assert.NoError(t, err)

	req := &csi.DeleteVolumeRequest{
		VolumeId: volumeID,
	}

	resp, err := service.DeleteVolume(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestDeleteVolume_NVMeOFVolume(t *testing.T) {
	mockClient := &mockAPIClient{
		deleteNVMeOFNamespaceFunc: func(_ context.Context, namespaceID int) error {
			assert.Equal(t, 20, namespaceID)
			return nil
		},
		deleteNVMeOFSubsystemFunc: func(_ context.Context, subsystemID int) error {
			assert.Equal(t, 10, subsystemID)
			return nil
		},
		deleteDatasetFunc: func(_ context.Context, datasetID string) error {
			assert.Equal(t, "test-zvol-id", datasetID)
			return nil
		},
	}
	service := NewControllerService(mockClient)

	// Create a valid NVMe-oF volume ID
	meta := VolumeMetadata{
		Name:              "test-volume",
		Protocol:          "nvmeof",
		DatasetID:         "test-zvol-id",
		DatasetName:       "tank/test-volume",
		NVMeOFSubsystemID: 10,
		NVMeOFNamespaceID: 20,
		NVMeOFNQN:         "nqn.test",
	}
	volumeID, err := encodeVolumeID(meta)
	assert.NoError(t, err)

	req := &csi.DeleteVolumeRequest{
		VolumeId: volumeID,
	}

	resp, err := service.DeleteVolume(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestValidateVolumeCapabilities(t *testing.T) {
	service := NewControllerService(nil)

	tests := []struct {
		name     string
		req      *csi.ValidateVolumeCapabilitiesRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "Missing volume ID",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           "",
				VolumeCapabilities: []*csi.VolumeCapability{{}},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Missing capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           "test-volume",
				VolumeCapabilities: nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Valid request",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{{}},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := service.ValidateVolumeCapabilities(context.Background(), tt.req)

			if tt.wantErr {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.wantCode, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Confirmed)
			}
		})
	}
}

func TestControllerPublishUnpublishVolume(t *testing.T) {
	service := NewControllerService(nil)

	// Test ControllerPublishVolume (should be no-op)
	publishResp, err := service.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, publishResp)

	// Test ControllerUnpublishVolume (should be no-op)
	unpublishResp, err := service.ControllerUnpublishVolume(context.Background(), &csi.ControllerUnpublishVolumeRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, unpublishResp)
}

func TestListVolumes(t *testing.T) {
	service := NewControllerService(nil)

	resp, err := service.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestGetCapacity(t *testing.T) {
	service := NewControllerService(nil)

	resp, err := service.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestUnimplementedMethods(t *testing.T) {
	service := NewControllerService(nil)
	ctx := context.Background()

	tests := []struct {
		name   string
		method func() error
	}{
		{
			name: "CreateSnapshot",
			method: func() error {
				_, err := service.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{})
				return err
			},
		},
		{
			name: "DeleteSnapshot",
			method: func() error {
				_, err := service.DeleteSnapshot(ctx, &csi.DeleteSnapshotRequest{})
				return err
			},
		},
		{
			name: "ListSnapshots",
			method: func() error {
				_, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{})
				return err
			},
		},
		{
			name: "ControllerExpandVolume",
			method: func() error {
				_, err := service.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{})
				return err
			},
		},
		{
			name: "ControllerGetVolume",
			method: func() error {
				_, err := service.ControllerGetVolume(ctx, &csi.ControllerGetVolumeRequest{})
				return err
			},
		},
		{
			name: "ControllerModifyVolume",
			method: func() error {
				_, err := service.ControllerModifyVolume(ctx, &csi.ControllerModifyVolumeRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.method()
			assert.Error(t, err)

			st, ok := status.FromError(err)
			assert.True(t, ok, "Error should be a gRPC status")
			assert.Equal(t, codes.Unimplemented, st.Code())
		})
	}
}
