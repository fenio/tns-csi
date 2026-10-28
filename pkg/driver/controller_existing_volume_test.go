package driver

import (
	"context"
	"testing"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// existingVolumeMock is a TrueNAS where the dataset for pvc-1 already exists with props
// and has a share at its mountpoint. It records whether any share or ACL was touched.
func existingVolumeMock(props map[string]string, touched *[]string) *MockAPIClientForSnapshots {
	const mountpoint = "/mnt/tank/pvc-1"
	return &MockAPIClientForSnapshots{
		GetDatasetPropertiesFunc: func(context.Context, string, []string) (map[string]string, error) {
			return props, nil
		},
		QueryNFSSharesFunc: func(context.Context, []interface{}) ([]tnsapi.NFSShare, error) {
			*touched = append(*touched, "query nfs share")
			return []tnsapi.NFSShare{{ID: 1, Path: mountpoint, Comment: "CSI Volume: pvc-1 | Capacity: 1073741824"}}, nil
		},
		QuerySMBShareFunc: func(context.Context, string) ([]tnsapi.SMBShare, error) {
			*touched = append(*touched, "query smb share")
			return []tnsapi.SMBShare{{ID: 2, Name: "pvc-1", Path: mountpoint, Comment: "CSI Volume: pvc-1 | Capacity: 1073741824"}}, nil
		},
		SetFilesystemACLFunc: func(_ context.Context, path string) error {
			*touched = append(*touched, "acl "+path)
			return nil
		},
		SetDatasetPropertiesFunc: func(context.Context, string, map[string]string) error {
			*touched = append(*touched, "set properties")
			return nil
		},
	}
}

// A dataset at the volume's path that carries no managed_by was not made by tns-csi: the
// existing-volume path must not complete it (SMB would grant everyone@ FULL_CONTROL on it).
func TestExistingShareVolumeRequiresManagedBy(t *testing.T) {
	dataset := &tnsapi.Dataset{ID: "tank/pvc-1", Name: "tank/pvc-1", Mountpoint: "/mnt/tank/pvc-1"}
	calls := map[string]func(*ControllerService) error{
		"nfs": func(s *ControllerService) error {
			_, _, err := s.handleExistingNFSVolume(context.Background(),
				&nfsVolumeParams{volumeName: "pvc-1", datasetName: dataset.ID, requestedCapacity: 1 << 30}, dataset,
				metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create"))
			return err
		},
		"smb": func(s *ControllerService) error {
			_, _, err := s.handleExistingSMBVolume(context.Background(),
				&smbVolumeParams{volumeName: "pvc-1", datasetName: dataset.ID, requestedCapacity: 1 << 30}, dataset,
				metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "create"))
			return err
		},
	}
	for proto, call := range calls {
		t.Run(proto+"/unmarked", func(t *testing.T) {
			var touched []string
			s := NewControllerService(existingVolumeMock(map[string]string{}, &touched), NewNodeRegistry(), "")
			if err := call(s); status.Code(err) != codes.AlreadyExists {
				t.Fatalf("error = %v, want AlreadyExists", err)
			}
			if len(touched) != 0 {
				t.Errorf("touched %v, want the unmarked dataset left alone", touched)
			}
		})
		t.Run(proto+"/owned", func(t *testing.T) {
			var touched []string
			props := map[string]string{tnsapi.PropertyManagedBy: tnsapi.ManagedByValue, tnsapi.PropertyCSIVolumeName: "pvc-1"}
			s := NewControllerService(existingVolumeMock(props, &touched), NewNodeRegistry(), "")
			if err := call(s); err != nil {
				t.Fatalf("error = %v, want the owned volume returned", err)
			}
		})
	}
}
