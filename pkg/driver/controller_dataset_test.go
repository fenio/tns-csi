package driver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func TestQueryExactDataset(t *testing.T) {
	const id = "tank/csi/nextcloud-html"
	exact := tnsapi.Dataset{ID: id, Name: id}
	sibling := tnsapi.Dataset{ID: id + "-iscsi", Name: id + "-iscsi"}
	child := tnsapi.Dataset{ID: id + "/child", Name: id + "/child"}
	queryErr := errors.New("query failed")
	for _, tt := range []struct {
		name     string
		datasets []tnsapi.Dataset
		err      error
		want     []tnsapi.Dataset
	}{
		{name: "absent"},
		{name: "prefix siblings only", datasets: []tnsapi.Dataset{sibling, child}},
		{name: "exact after siblings", datasets: []tnsapi.Dataset{sibling, child, exact}, want: []tnsapi.Dataset{exact}},
		{name: "exact first", datasets: []tnsapi.Dataset{exact, sibling}, want: []tnsapi.Dataset{exact}},
		{name: "name alone is not identity", datasets: []tnsapi.Dataset{{ID: sibling.ID, Name: id}}},
		{name: "query failure", datasets: []tnsapi.Dataset{exact}, err: queryErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := &MockAPIClientForSnapshots{QueryAllDatasetsFunc: func(_ context.Context, prefix string) ([]tnsapi.Dataset, error) {
				if prefix != id {
					t.Fatalf("prefix = %q, want %q", prefix, id)
				}
				return tt.datasets, tt.err
			}}
			s := &ControllerService{apiClient: m}
			got, err := s.queryExactDataset(context.Background(), id)
			if !errors.Is(err, tt.err) || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v, %v", got, err, tt.want, tt.err)
			}
		})
	}
}

// Exercise each protocol's real create path, not only the shared filter. Stop at
// dataset creation so no share, extent or target can hide a wrong existence check.
func TestCreateVolumeDoesNotReusePrefixSibling(t *testing.T) {
	const id = "tank/csi/nextcloud-html"
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			m := &MockAPIClientForSnapshots{}
			m.QueryAllDatasetsFunc = func(_ context.Context, prefix string) ([]tnsapi.Dataset, error) {
				if prefix != id {
					t.Fatalf("unexpected dataset query: %s", prefix)
				}
				return []tnsapi.Dataset{{ID: id + "-iscsi", Name: id + "-iscsi", Type: "VOLUME", Volsize: map[string]interface{}{"parsed": float64(1 << 30)}}}, nil
			}
			m.GetDatasetPropertiesFunc = func(context.Context, string, []string) (map[string]string, error) {
				t.Fatal("must not read properties of prefix sibling")
				return nil, errors.New("unexpected property read")
			}
			created := 0
			stop := errors.New("stop after exact dataset create")
			m.CreateDatasetFunc = func(_ context.Context, p tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
				if p.Name != id {
					t.Fatalf("creating wrong dataset: %s", p.Name)
				}
				created++
				return nil, stop
			}
			m.CreateZvolFunc = func(_ context.Context, p tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
				if p.Name != id {
					t.Fatalf("creating wrong ZVOL: %s", p.Name)
				}
				created++
				return nil, stop
			}
			m.DeleteDatasetFunc = func(context.Context, string) error {
				t.Fatal("must not delete prefix sibling")
				return nil
			}
			s := &ControllerService{apiClient: m}
			_, err := s.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
				Name:          "nextcloud-html",
				Parameters:    map[string]string{"protocol": protocol, "pool": "tank", "parentDataset": "tank/csi", "server": "192.0.2.10"},
				CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
				VolumeCapabilities: []*csi.VolumeCapability{{
					AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}},
					AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
				}},
			})
			if created != 1 || err == nil || !strings.Contains(err.Error(), stop.Error()) {
				t.Fatalf("want exact dataset creation; created=%d, err=%v", created, err)
			}
		})
	}
}

func TestBlockVolumeHealthUsesExactDataset(t *testing.T) {
	const id = "tank/csi/nextcloud-html"
	sibling := tnsapi.Dataset{ID: id + "-iscsi", Name: id + "-iscsi", Volsize: map[string]interface{}{"parsed": float64(2 << 30)}}
	exact := tnsapi.Dataset{ID: id, Name: id, Volsize: map[string]interface{}{"parsed": float64(1 << 30)}}
	for _, protocol := range []string{ProtocolISCSI, ProtocolNVMeOF} {
		for _, tt := range []struct {
			name     string
			datasets []tnsapi.Dataset
			capacity int64
			missing  bool
		}{
			{name: "sibling only", datasets: []tnsapi.Dataset{sibling}, missing: true},
			{name: "exact after sibling", datasets: []tnsapi.Dataset{sibling, exact}, capacity: 1 << 30},
		} {
			t.Run(protocol+"/"+tt.name, func(t *testing.T) {
				m := &MockAPIClientForSnapshots{QueryAllDatasetsFunc: func(context.Context, string) ([]tnsapi.Dataset, error) {
					return tt.datasets, nil
				}}
				s := &ControllerService{apiClient: m}
				meta := &VolumeMetadata{Name: "nextcloud-html", DatasetName: id, Protocol: protocol}
				var resp *csi.ControllerGetVolumeResponse
				var health VolumeHealth
				var err error
				if protocol == ProtocolISCSI {
					resp, health, err = s.getISCSIVolumeInfo(context.Background(), meta)
				} else {
					resp, health, err = s.getNVMeOFVolumeInfo(context.Background(), meta)
				}
				if err != nil {
					t.Fatal(err)
				}
				if resp.Volume.CapacityBytes != tt.capacity || strings.Contains(health.Message, "ZVOL "+id+" not found") != tt.missing {
					t.Fatalf("wrong dataset health: capacity=%d, health=%+v", resp.Volume.CapacityBytes, health)
				}
			})
		}
	}
}
