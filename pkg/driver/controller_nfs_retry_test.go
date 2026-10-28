package driver

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	nfsRetryVolume   = "pvc-nfs-retry"
	nfsRetryDataset  = "tank/csi/" + nfsRetryVolume
	nfsRetryMount    = "/mnt/" + nfsRetryDataset
	nfsRetryCapacity = int64(2 << 30)
)

// nfsRetryFake is a minimal stateful TrueNAS for one NFS volume whose dataset exists, as
// left by an earlier CreateVolume that was cut off before it recorded the share ID.
type nfsRetryFake struct {
	share   *tnsapi.NFSShare
	props   map[string]string
	deletes []int
	creates int
	mu      sync.Mutex
}

func newNFSRetryFake(owner string, withShare bool) *nfsRetryFake {
	f := &nfsRetryFake{props: map[string]string{
		tnsapi.PropertyManagedBy:     tnsapi.ManagedByValue,
		tnsapi.PropertyCSIVolumeName: owner,
	}}
	if withShare {
		f.share = &tnsapi.NFSShare{ID: 42, Path: nfsRetryMount, Comment: "CSI Volume: " + owner + " | Capacity: " + strconv.FormatInt(nfsRetryCapacity, 10)}
	}
	return f
}

func (f *nfsRetryFake) mock() *MockAPIClientForSnapshots {
	return &MockAPIClientForSnapshots{
		GetDatasetFunc: func(context.Context, string) (*tnsapi.Dataset, error) {
			return &tnsapi.Dataset{ID: nfsRetryDataset, Name: nfsRetryDataset, Type: "FILESYSTEM", Mountpoint: nfsRetryMount}, nil
		},
		GetDatasetPropertiesFunc: func(_ context.Context, _ string, names []string) (map[string]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := map[string]string{}
			for _, n := range names {
				if v, ok := f.props[n]; ok {
					out[n] = v
				}
			}
			return out, nil
		},
		SetDatasetPropertiesFunc: func(_ context.Context, _ string, props map[string]string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			for k, v := range props {
				f.props[k] = v
			}
			return nil
		},
		QueryNFSSharesFunc: func(context.Context, []interface{}) ([]tnsapi.NFSShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.share == nil {
				return nil, nil
			}
			return []tnsapi.NFSShare{*f.share}, nil
		},
		QueryNFSShareFunc: func(context.Context, string) ([]tnsapi.NFSShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.share == nil {
				return nil, nil
			}
			return []tnsapi.NFSShare{*f.share}, nil
		},
		CreateNFSShareFunc: func(_ context.Context, p tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates++
			f.share = &tnsapi.NFSShare{ID: 43, Path: p.Path, Comment: p.Comment}
			return f.share, nil
		},
		DeleteNFSShareFunc: func(_ context.Context, id int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deletes = append(f.deletes, id)
			return nil
		},
		DeleteDatasetFunc: func(context.Context, string) error { return nil },
	}
}

func nfsRetryRequest() *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:          nfsRetryVolume,
		Parameters:    map[string]string{"protocol": "nfs", "pool": "tank", "parentDataset": "tank/csi", "server": "10.0.0.1"},
		CapacityRange: &csi.CapacityRange{RequiredBytes: nfsRetryCapacity},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		}},
	}
}

// A CreateVolume cut off between CreateNFSShare and the property write leaves a dataset
// with ownership properties but no nfs_share_id. The retry must record the share (found
// by path, or created when missing) so DeleteVolume, which reads only properties, can
// remove the export.
func TestCreateNFSVolumeRetryRecordsShareID(t *testing.T) {
	cases := map[string]struct {
		wantShareID string
		wantCreates int
		withShare   bool
	}{
		"share exists":  {withShare: true, wantShareID: "42", wantCreates: 0},
		"share missing": {withShare: false, wantShareID: "43", wantCreates: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newNFSRetryFake(nfsRetryVolume, tc.withShare)
			s := NewControllerService(fake.mock(), NewNodeRegistry(), "")

			resp, err := s.CreateVolume(context.Background(), nfsRetryRequest())
			if err != nil {
				t.Fatalf("CreateVolume() error = %v", err)
			}
			if got := fake.props[tnsapi.PropertyNFSShareID]; got != tc.wantShareID {
				t.Errorf("%s = %q, want %q", tnsapi.PropertyNFSShareID, got, tc.wantShareID)
			}
			if fake.creates != tc.wantCreates {
				t.Errorf("CreateNFSShare calls = %d, want %d", fake.creates, tc.wantCreates)
			}
			if resp.GetVolume().GetCapacityBytes() != nfsRetryCapacity {
				t.Errorf("capacity = %d, want %d", resp.GetVolume().GetCapacityBytes(), nfsRetryCapacity)
			}

			// The follow-on delete removes the recorded share.
			shareID, convErr := strconv.Atoi(resp.GetVolume().GetVolumeContext()["nfsShareID"])
			if convErr != nil {
				t.Fatalf("volume context has no usable nfsShareID: %v", resp.GetVolume().GetVolumeContext())
			}
			meta := &VolumeMetadata{Name: nfsRetryDataset, Protocol: ProtocolNFS, DatasetID: nfsRetryDataset, DatasetName: nfsRetryDataset, NFSShareID: shareID}
			if _, err := s.deleteNFSVolume(context.Background(), meta); err != nil {
				t.Fatalf("deleteNFSVolume() error = %v", err)
			}
			if len(fake.deletes) != 1 || strconv.Itoa(fake.deletes[0]) != tc.wantShareID {
				t.Errorf("DeleteNFSShare calls = %v, want [%s]", fake.deletes, tc.wantShareID)
			}
		})
	}
}

// A dataset at the volume's path that belongs to another volume or manager is never
// adopted, whether or not it has a share.
func TestCreateNFSVolumeRetryRejectsOtherOwner(t *testing.T) {
	for _, withShare := range []bool{true, false} {
		fake := newNFSRetryFake("some-other-volume", withShare)
		s := NewControllerService(fake.mock(), NewNodeRegistry(), "")

		_, err := s.CreateVolume(context.Background(), nfsRetryRequest())
		if status.Code(err) != codes.AlreadyExists {
			t.Errorf("withShare=%v: CreateVolume() error = %v, want AlreadyExists", withShare, err)
		}
		if fake.creates != 0 || fake.props[tnsapi.PropertyNFSShareID] != "" {
			t.Errorf("withShare=%v: touched a dataset owned by someone else (creates=%d, props=%v)", withShare, fake.creates, fake.props)
		}
	}
}

// The retry reports the volume's existing capacity and refuses an incompatible size.
func TestCreateNFSVolumeRetryChecksCapacity(t *testing.T) {
	fake := newNFSRetryFake(nfsRetryVolume, true)
	s := NewControllerService(fake.mock(), NewNodeRegistry(), "")
	req := nfsRetryRequest()
	req.CapacityRange.RequiredBytes = nfsRetryCapacity * 2

	_, err := s.CreateVolume(context.Background(), req)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("CreateVolume() error = %v, want AlreadyExists", err)
	}
}
