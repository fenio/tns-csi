package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func cloneFromVolumeRequest(name, source string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:          name,
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
		Parameters: map[string]string{"pool": "tank", "parentDataset": "csi", "protocol": ProtocolNFS, "server": "truenas.example"},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: source}},
		},
	}
}

// The external-provisioner retries CreateVolume with the same name after a timeout.
// The temporary snapshot used to clone a volume has a deterministic name, so the
// retry must reuse the one the first attempt left behind instead of failing on
// "already exists" forever.
func TestCreateVolumeFromVolumeReusesExistingTempSnapshot(t *testing.T) {
	const source = "tank/csi/pvc-src"
	tempFullName := source + "@" + VolumeSourceSnapshotPrefix + "pvc-new"
	cloneErr := errors.New("stop here: clone is not under test")

	created := 0
	mock := &MockAPIClientForSnapshots{
		GetDatasetFunc: sourceDatasetOnly(source),
		QuerySnapshotsFunc: func(context.Context, []interface{}) ([]tnsapi.Snapshot, error) {
			return []tnsapi.Snapshot{{ID: tempFullName, Name: tempFullName, Dataset: source}}, nil
		},
		CreateSnapshotFunc: func(_ context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
			created++
			return nil, errors.New("[EEXIST] snapshot already exists")
		},
		CloneSnapshotFunc: func(context.Context, tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
			return nil, cloneErr
		},
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")

	_, err := s.createVolumeFromVolume(context.Background(), cloneFromVolumeRequest("pvc-new", source), source)
	if err == nil || !errorContains(err, cloneErr.Error()) {
		t.Fatalf("createVolumeFromVolume() error = %v, want the clone error (temp snapshot must have been reused)", err)
	}
	if created != 0 {
		t.Errorf("CreateSnapshot called %d time(s) although %s already exists", created, tempFullName)
	}
}

// A snapshot create that races with the previous attempt (or whose earlier response
// was lost) comes back as EEXIST. That is the same snapshot, so use it.
func TestCreateVolumeFromVolumeTreatsSnapshotEEXISTAsReuse(t *testing.T) {
	const source = "tank/csi/pvc-src"
	cloneErr := errors.New("stop here: clone is not under test")

	mock := &MockAPIClientForSnapshots{
		GetDatasetFunc: sourceDatasetOnly(source),
		QuerySnapshotsFunc: func(context.Context, []interface{}) ([]tnsapi.Snapshot, error) {
			return nil, nil // not visible yet
		},
		CreateSnapshotFunc: func(context.Context, tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
			return nil, errors.New("[EEXIST] snapshot already exists")
		},
		CloneSnapshotFunc: func(context.Context, tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
			return nil, cloneErr
		},
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")

	_, err := s.createVolumeFromVolume(context.Background(), cloneFromVolumeRequest("pvc-new", source), source)
	if err == nil || !errorContains(err, cloneErr.Error()) {
		t.Fatalf("createVolumeFromVolume() error = %v, want the clone error (EEXIST must mean reuse)", err)
	}
}

// sourceDatasetOnly describes a TrueNAS where only the source volume's dataset exists (the
// clone target does not yet).
func sourceDatasetOnly(source string) func(context.Context, string) (*tnsapi.Dataset, error) {
	return func(_ context.Context, id string) (*tnsapi.Dataset, error) {
		if id != source {
			return nil, tnsapi.ErrDatasetNotFound
		}
		return &tnsapi.Dataset{ID: id, Name: id, Type: "FILESYSTEM"}, nil
	}
}

func errorContains(err error, s string) bool {
	return err != nil && strings.Contains(err.Error(), s)
}
