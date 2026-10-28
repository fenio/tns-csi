package driver

import (
	"context"
	"strconv"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	smbRetryVolume   = "pvc-smb-retry"
	smbRetryDataset  = "tank/csi/" + smbRetryVolume
	smbRetryMount    = "/mnt/" + smbRetryDataset
	smbRetryCapacity = int64(2 << 30)
)

// smbRetryMock describes a dataset and SMB share left by a CreateVolume cut off before
// the ACL was applied. It returns the mock and a pointer to the paths SetFilesystemACL saw.
func smbRetryMock(owner string) (mock *MockAPIClientForSnapshots, aclPathsSeen *[]string) {
	props := map[string]string{
		tnsapi.PropertyManagedBy:     tnsapi.ManagedByValue,
		tnsapi.PropertyCSIVolumeName: owner,
		tnsapi.PropertySMBShareID:    "7",
	}
	var aclPaths []string
	return &MockAPIClientForSnapshots{
		GetDatasetFunc: func(context.Context, string) (*tnsapi.Dataset, error) {
			return &tnsapi.Dataset{ID: smbRetryDataset, Name: smbRetryDataset, Type: "FILESYSTEM", Mountpoint: smbRetryMount}, nil
		},
		GetDatasetPropertiesFunc: func(_ context.Context, _ string, names []string) (map[string]string, error) {
			out := map[string]string{}
			for _, n := range names {
				if v, ok := props[n]; ok {
					out[n] = v
				}
			}
			return out, nil
		},
		SetDatasetPropertiesFunc: func(context.Context, string, map[string]string) error { return nil },
		QuerySMBShareFunc: func(context.Context, string) ([]tnsapi.SMBShare, error) {
			return []tnsapi.SMBShare{{
				ID: 7, Name: smbRetryVolume, Path: smbRetryMount,
				Comment: "CSI Volume: " + smbRetryVolume + " | Capacity: " + strconv.FormatInt(smbRetryCapacity, 10),
			}}, nil
		},
		SetFilesystemACLFunc: func(_ context.Context, path string) error {
			aclPaths = append(aclPaths, path)
			return nil
		},
	}, &aclPaths
}

func smbRetryRequest(capacity int64) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:          smbRetryVolume,
		Parameters:    map[string]string{"protocol": "smb", "pool": "tank", "parentDataset": "tank/csi", "server": "10.0.0.1"},
		CapacityRange: &csi.CapacityRange{RequiredBytes: capacity},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		}},
	}
}

// A retry finding the dataset and share must re-apply the ACL the first attempt may not have reached.
func TestCreateSMBVolumeRetryAppliesACL(t *testing.T) {
	m, aclPaths := smbRetryMock(smbRetryVolume)
	s := NewControllerService(m, NewNodeRegistry(), "")

	resp, err := s.CreateVolume(context.Background(), smbRetryRequest(smbRetryCapacity))
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if len(*aclPaths) != 1 || (*aclPaths)[0] != smbRetryMount {
		t.Errorf("SetFilesystemACL calls = %v, want [%s]", *aclPaths, smbRetryMount)
	}
	if resp.GetVolume().GetCapacityBytes() != smbRetryCapacity {
		t.Errorf("capacity = %d, want %d", resp.GetVolume().GetCapacityBytes(), smbRetryCapacity)
	}
}

// An incompatible size is AlreadyExists, and nothing is modified.
func TestCreateSMBVolumeRetryChecksCapacity(t *testing.T) {
	m, aclPaths := smbRetryMock(smbRetryVolume)
	s := NewControllerService(m, NewNodeRegistry(), "")

	_, err := s.CreateVolume(context.Background(), smbRetryRequest(smbRetryCapacity*2))
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("CreateVolume() error = %v, want AlreadyExists", err)
	}
	if len(*aclPaths) != 0 {
		t.Errorf("SetFilesystemACL called %v for a rejected request", *aclPaths)
	}
}

// A dataset belonging to another volume is never completed.
func TestCreateSMBVolumeRetryRejectsOtherOwner(t *testing.T) {
	m, aclPaths := smbRetryMock("some-other-volume")
	s := NewControllerService(m, NewNodeRegistry(), "")

	_, err := s.CreateVolume(context.Background(), smbRetryRequest(smbRetryCapacity))
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("CreateVolume() error = %v, want AlreadyExists", err)
	}
	if len(*aclPaths) != 0 {
		t.Errorf("SetFilesystemACL called %v on a foreign dataset", *aclPaths)
	}
}
