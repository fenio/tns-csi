package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// filtersJSON renders query-filters the way they go over the wire.
func filtersJSON(t *testing.T, filters []interface{}) string {
	t.Helper()
	b, err := json.Marshal(filters)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// blanketQueryMock fails the test if any caller falls back to an unfiltered list query;
// the specific lookups below must ask TrueNAS for exactly what they need.
func blanketQueryMock(t *testing.T) *MockAPIClientForSnapshots {
	t.Helper()
	return &MockAPIClientForSnapshots{
		QueryAllNFSSharesFunc: func(context.Context, string) ([]tnsapi.NFSShare, error) {
			t.Error("unfiltered sharing.nfs.query")
			return nil, nil
		},
		QueryAllNVMeOFNamespacesFunc: func(context.Context) ([]tnsapi.NVMeOFNamespace, error) {
			t.Error("unfiltered nvmet.namespace.query")
			return nil, nil
		},
		ListAllNVMeOFSubsystemsFunc: func(context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
			t.Error("unfiltered nvmet.subsys.query")
			return nil, nil
		},
	}
}

func TestSpecificLookupsFilterOnServer(t *testing.T) {
	t.Run("existing NVMe-oF namespace by subsystem", func(t *testing.T) {
		mock := blanketQueryMock(t)
		var got string
		mock.QueryNVMeOFNamespacesFunc = func(_ context.Context, f []interface{}) ([]tnsapi.NVMeOFNamespace, error) {
			got = filtersJSON(t, f)
			return nil, nil
		}
		s := NewControllerService(mock, NewNodeRegistry(), "")
		if _, err := s.findExistingNVMeOFNamespace(context.Background(), "zvol/tank/pvc-1", 7); err != nil {
			t.Fatal(err)
		}
		if want := `[["subsys_id","=",7]]`; got != want {
			t.Fatalf("namespace filters = %s, want %s", got, want)
		}
	})

	t.Run("namespaces attached to a subsystem before deleting it", func(t *testing.T) {
		mock := blanketQueryMock(t)
		var got string
		mock.QueryNVMeOFNamespacesFunc = func(_ context.Context, f []interface{}) ([]tnsapi.NVMeOFNamespace, error) {
			got = filtersJSON(t, f)
			return nil, nil
		}
		mock.DeleteNVMeOFSubsystemFunc = func(context.Context, int) error { return nil }
		s := NewControllerService(mock, NewNodeRegistry(), "")
		meta := ownedMeta(mock, &VolumeMetadata{Name: "pvc-9", DatasetID: "tank/pvc-9", NVMeOFSubsystemID: 9})
		if err := s.deleteNVMeOFSubsystem(context.Background(), meta); err != nil {
			t.Fatal(err)
		}
		if want := `[["subsys_id","=",9]]`; got != want {
			t.Fatalf("namespace filters = %s, want %s", got, want)
		}
	})

	t.Run("existing NFS share by mountpoint", func(t *testing.T) {
		mock := blanketQueryMock(t)
		var got string
		mock.QueryNFSSharesFunc = func(_ context.Context, f []interface{}) ([]tnsapi.NFSShare, error) {
			got = filtersJSON(t, f)
			return nil, nil
		}
		mock.GetDatasetPropertiesFunc = func(context.Context, string, []string) (map[string]string, error) {
			return map[string]string{tnsapi.PropertyManagedBy: tnsapi.ManagedByValue}, nil
		}
		s := NewControllerService(mock, NewNodeRegistry(), "")
		ds := &tnsapi.Dataset{ID: "tank/pvc-1", Name: "tank/pvc-1", Mountpoint: "/mnt/tank/pvc-1"}
		if _, _, err := s.handleExistingNFSVolume(context.Background(), &nfsVolumeParams{volumeName: "pvc-1", datasetName: "tank/pvc-1"}, ds,
			metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create")); err != nil {
			t.Fatal(err)
		}
		if want := `[["path","=","/mnt/tank/pvc-1"]]`; got != want {
			t.Fatalf("share filters = %s, want %s", got, want)
		}
	})

	t.Run("NVMe-oF subsystem health by ID", func(t *testing.T) {
		mock := blanketQueryMock(t)
		var got string
		mock.QueryNVMeOFSubsystemsFunc = func(_ context.Context, f []interface{}) ([]tnsapi.NVMeOFSubsystem, error) {
			got = filtersJSON(t, f)
			return []tnsapi.NVMeOFSubsystem{{ID: 7}}, nil
		}
		mock.QueryAllDatasetsFunc = func(context.Context, string) ([]tnsapi.Dataset, error) {
			return []tnsapi.Dataset{{ID: "tank/pvc-1", Name: "tank/pvc-1"}}, nil
		}
		s := NewControllerService(mock, NewNodeRegistry(), "")
		_, health, err := s.getNVMeOFVolumeInfo(context.Background(), &VolumeMetadata{Name: "pvc-1", DatasetName: "tank/pvc-1", NVMeOFSubsystemID: 7})
		if err != nil {
			t.Fatal(err)
		}
		if want := `[["id","=",7]]`; got != want {
			t.Fatalf("subsystem filters = %s, want %s", got, want)
		}
		if health.Abnormal {
			t.Fatalf("subsystem found by ID reported abnormal: %s", health.Message)
		}
	})

	t.Run("legacy volume discovery by share path and extent disk", func(t *testing.T) {
		mock := blanketQueryMock(t)
		var nfs, smb, extents string
		mock.QueryNFSSharesFunc = func(_ context.Context, f []interface{}) ([]tnsapi.NFSShare, error) {
			nfs = filtersJSON(t, f)
			return nil, nil
		}
		mock.QuerySMBSharesFunc = func(_ context.Context, f []interface{}) ([]tnsapi.SMBShare, error) {
			smb = filtersJSON(t, f)
			return nil, nil
		}
		mock.QueryNVMeOFNamespacesFunc = func(context.Context, []interface{}) ([]tnsapi.NVMeOFNamespace, error) { return nil, nil }
		mock.QueryISCSIExtentsFunc = func(_ context.Context, f []interface{}) ([]tnsapi.ISCSIExtent, error) {
			extents = filtersJSON(t, f)
			return nil, nil
		}
		s := NewControllerService(mock, NewNodeRegistry(), "")
		if res := s.discoverVolumeBySearching(context.Background(), "pvc-legacy"); res != nil {
			t.Fatalf("unexpected discovery result %+v", res)
		}
		if want := `[["path","$","/pvc-legacy"]]`; nfs != want || smb != want {
			t.Fatalf("share filters nfs=%s smb=%s, want %s", nfs, smb, want)
		}
		if want := `[["disk","$","/pvc-legacy"]]`; extents != want {
			t.Fatalf("extent filters = %s, want %s", extents, want)
		}
	})
}
