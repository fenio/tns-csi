package driver

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	cloneRetryName   = "pvc-new"
	cloneRetryTarget = "tank/csi/" + cloneRetryName
	cloneRetryMount  = "/mnt/" + cloneRetryTarget
	cloneRetrySource = "tank/csi/pvc-src"
	cloneRetrySnap   = cloneRetrySource + "@snap1"
)

// cloneRetryFake is a minimal stateful TrueNAS holding the clone target of one
// CreateVolume-from-snapshot request and the presentation objects built on it.
type cloneRetryFake struct {
	target       *tnsapi.Dataset // nil: the clone target does not exist
	props        map[string]string
	nfsShare     *tnsapi.NFSShare
	smbShare     *tnsapi.SMBShare
	iscsiExtent  *tnsapi.ISCSIExtent
	iscsiTarget  *tnsapi.ISCSITarget
	cloneErr     error // when set, CloneSnapshot makes the target and returns this error
	promoteErr   error // when set, PromoteDataset promotes and then returns this error
	deleteErr    error // when set, DeleteDataset returns it and deletes nothing
	creates      map[string]int
	deletes      []string
	staleCtx     []string // deletes that ran on an already-canceled context
	clones       int
	promotes     int
	iscsiMapped  bool
	hiddenTarget bool // the target exists but lookups miss it until CloneSnapshot is called
	mu           sync.Mutex
}

func newCloneRetryFake() *cloneRetryFake {
	return &cloneRetryFake{props: map[string]string{}, creates: map[string]int{}}
}

// withTarget gives the fake a clone target that an earlier attempt of this CreateVolume
// made: it carries the ownership properties the clone call writes.
func (f *cloneRetryFake) withTarget(datasetType string) *cloneRetryFake {
	f.target = &tnsapi.Dataset{ID: cloneRetryTarget, Name: cloneRetryTarget, Type: datasetType, Mountpoint: cloneRetryMount}
	if datasetType == datasetTypeVolume {
		f.target.Mountpoint = ""
		f.target.Volsize = map[string]interface{}{"parsed": float64(1 << 30)}
	}
	f.props[tnsapi.PropertyManagedBy] = tnsapi.ManagedByValue
	f.props[tnsapi.PropertyCSIVolumeName] = cloneRetryName
	return f
}

// withPresentation adds the share/target/extent the earlier attempt built for protocol.
func (f *cloneRetryFake) withPresentation(protocol string) *cloneRetryFake {
	switch protocol {
	case ProtocolNFS:
		f.nfsShare = &tnsapi.NFSShare{ID: 10, Path: cloneRetryMount, Enabled: true}
	case ProtocolSMB:
		f.smbShare = &tnsapi.SMBShare{ID: 20, Name: cloneRetryName, Path: cloneRetryMount, Enabled: true}
	case ProtocolISCSI:
		f.iscsiExtent = &tnsapi.ISCSIExtent{ID: 30, Name: cloneRetryName, Disk: "zvol/" + cloneRetryTarget}
		f.iscsiTarget = &tnsapi.ISCSITarget{ID: 31, Name: cloneRetryName}
		f.iscsiMapped = true
	}
	return f
}

func (f *cloneRetryFake) visibleTarget() *tnsapi.Dataset {
	if f.target == nil || (f.hiddenTarget && f.clones == 0) {
		return nil
	}
	ds := *f.target
	return &ds
}

func (f *cloneRetryFake) mock() *MockAPIClientForSnapshots {
	return &MockAPIClientForSnapshots{
		GetDatasetFunc: func(_ context.Context, id string) (*tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if ds := f.visibleTarget(); ds != nil && ds.ID == id {
				return ds, nil
			}
			return nil, tnsapi.ErrDatasetNotFound
		},
		GetDatasetPropertiesFunc: func(_ context.Context, id string, names []string) (map[string]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if ds := f.visibleTarget(); ds == nil || ds.ID != id {
				return nil, tnsapi.ErrDatasetNotFound
			}
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
		UpdateDatasetFunc: func(_ context.Context, _ string, _ tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.visibleTarget(), nil
		},
		CloneSnapshotFunc: func(_ context.Context, p tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.clones++
			if f.cloneErr != nil {
				return nil, f.cloneErr
			}
			f.target = &tnsapi.Dataset{ID: p.Dataset, Name: p.Dataset, Type: "FILESYSTEM", Mountpoint: "/mnt/" + p.Dataset,
				Origin: map[string]interface{}{"value": p.Snapshot}}
			for k, v := range p.DatasetProperties {
				f.props[k] = v
			}
			ds := *f.target
			return &ds, nil
		},
		PromoteDatasetFunc: func(_ context.Context, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.promotes++
			if f.target != nil {
				f.target.Origin = map[string]interface{}{"value": ""}
			}
			return f.promoteErr
		},
		DeleteDatasetFunc: func(ctx context.Context, id string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deletes = append(f.deletes, id)
			if ctx.Err() != nil {
				f.staleCtx = append(f.staleCtx, id)
			}
			if f.deleteErr != nil {
				return f.deleteErr
			}
			f.target = nil
			return nil
		},
		DeleteSnapshotFunc: func(context.Context, string) error { return nil },
		QueryNFSSharesFunc: func(context.Context, []interface{}) ([]tnsapi.NFSShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.nfsShare == nil {
				return nil, nil
			}
			return []tnsapi.NFSShare{*f.nfsShare}, nil
		},
		CreateNFSShareFunc: func(_ context.Context, p tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["nfs share"]++
			f.nfsShare = &tnsapi.NFSShare{ID: 11, Path: p.Path, Enabled: true}
			return f.nfsShare, nil
		},
		QuerySMBShareFunc: func(context.Context, string) ([]tnsapi.SMBShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.smbShare == nil {
				return nil, nil
			}
			return []tnsapi.SMBShare{*f.smbShare}, nil
		},
		CreateSMBShareFunc: func(_ context.Context, p tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["smb share"]++
			f.smbShare = &tnsapi.SMBShare{ID: 21, Name: p.Name, Path: p.Path, Enabled: p.Enabled}
			return f.smbShare, nil
		},
		ISCSIExtentByNameFunc: func(context.Context, string) (*tnsapi.ISCSIExtent, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.iscsiExtent, nil
		},
		ISCSITargetByNameFunc: func(context.Context, string) (*tnsapi.ISCSITarget, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.iscsiTarget, nil
		},
		ISCSITargetExtentByTargetFunc: func(context.Context, int) ([]tnsapi.ISCSITargetExtent, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.iscsiMapped {
				return nil, nil
			}
			return []tnsapi.ISCSITargetExtent{{ID: 32, Target: f.iscsiTarget.ID, Extent: f.iscsiExtent.ID, LunID: 0}}, nil
		},
		CreateISCSIExtentFunc: func(_ context.Context, p tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["iscsi extent"]++
			f.iscsiExtent = &tnsapi.ISCSIExtent{ID: 33, Name: p.Name, Disk: p.Disk}
			return f.iscsiExtent, nil
		},
		CreateISCSITargetFunc: func(_ context.Context, p tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["iscsi target"]++
			f.iscsiTarget = &tnsapi.ISCSITarget{ID: 34, Name: p.Name}
			return f.iscsiTarget, nil
		},
		CreateISCSITargetExtentFunc: func(_ context.Context, p tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["iscsi target-extent"]++
			f.iscsiMapped = true
			return &tnsapi.ISCSITargetExtent{ID: 35, Target: p.Target, Extent: p.Extent}, nil
		},
	}
}

func cloneRetryRequest(protocol string, extra map[string]string) (req *csi.CreateVolumeRequest, snapshotID string) {
	params := map[string]string{"pool": "tank", "parentDataset": "csi", "server": "truenas.example", "protocol": protocol}
	for k, v := range extra {
		params[k] = v
	}
	snapshotID = protocol + ":" + cloneRetrySnap
	return &csi.CreateVolumeRequest{
		Name:          cloneRetryName,
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		Parameters:    params,
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: snapshotID}},
		},
	}, snapshotID
}

func checkClonedVolume(t *testing.T, resp *csi.CreateVolumeResponse, snapshotID string) {
	t.Helper()
	if got := resp.GetVolume().GetVolumeId(); got != cloneRetryTarget {
		t.Errorf("volume ID = %q, want %q", got, cloneRetryTarget)
	}
	if got := resp.GetVolume().GetContentSource().GetSnapshot().GetSnapshotId(); got != snapshotID {
		t.Errorf("content source snapshot = %q, want %q", got, snapshotID)
	}
}

// A retried CreateVolume from a snapshot finds the clone the earlier attempt made. It must
// complete that volume (or return it as is) instead of cloning again and, when the clone
// reports "already exists", deleting the earlier attempt's dataset.
func TestCreateVolumeFromSnapshotResumesExistingClone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		wantCreates  map[string]int
		name         string
		protocol     string
		datasetType  string
		presentation bool
	}{
		{name: "nfs, share left over", protocol: ProtocolNFS, datasetType: "FILESYSTEM", presentation: true, wantCreates: map[string]int{}},
		{name: "nfs, dataset only", protocol: ProtocolNFS, datasetType: "FILESYSTEM", wantCreates: map[string]int{"nfs share": 1}},
		{name: "smb, share left over", protocol: ProtocolSMB, datasetType: "FILESYSTEM", presentation: true, wantCreates: map[string]int{}},
		{name: "smb, dataset only", protocol: ProtocolSMB, datasetType: "FILESYSTEM", wantCreates: map[string]int{"smb share": 1}},
		{name: "iscsi, target and extent left over", protocol: ProtocolISCSI, datasetType: datasetTypeVolume, presentation: true, wantCreates: map[string]int{}},
		{
			name: "iscsi, zvol only", protocol: ProtocolISCSI, datasetType: datasetTypeVolume,
			wantCreates: map[string]int{"iscsi extent": 1, "iscsi target": 1, "iscsi target-extent": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCloneRetryFake().withTarget(tt.datasetType)
			if tt.presentation {
				f.withPresentation(tt.protocol)
			}
			req, snapshotID := cloneRetryRequest(tt.protocol, nil)

			resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
			if err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			checkClonedVolume(t, resp, snapshotID)
			if f.clones != 0 {
				t.Errorf("CloneSnapshot called %d time(s) although the clone target exists", f.clones)
			}
			if len(f.deletes) != 0 {
				t.Errorf("retry deleted %v", f.deletes)
			}
			if !reflect.DeepEqual(f.creates, tt.wantCreates) {
				t.Errorf("create calls = %v, want %v (objects already present must be reused)", f.creates, tt.wantCreates)
			}
			if got := f.props[tnsapi.PropertyContentSourceID]; got != snapshotID {
				t.Errorf("content source property = %q, want %q", got, snapshotID)
			}
		})
	}
}

// A dataset at the clone target that is not this volume's is never cloned over, adopted
// or deleted.
func TestCreateVolumeFromSnapshotRejectsForeignCloneTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		props map[string]string
		name  string
	}{
		{name: "another volume's dataset", props: map[string]string{
			tnsapi.PropertyManagedBy: tnsapi.ManagedByValue, tnsapi.PropertyCSIVolumeName: "pvc-other",
		}},
		{name: "managed by something else", props: map[string]string{
			tnsapi.PropertyManagedBy: "someone-else", tnsapi.PropertyCSIVolumeName: cloneRetryName,
		}},
		{name: "no ownership properties", props: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCloneRetryFake().withTarget("FILESYSTEM").withPresentation(ProtocolNFS)
			f.props = tt.props
			req, snapshotID := cloneRetryRequest(ProtocolNFS, nil)

			_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
			if status.Code(err) != codes.AlreadyExists {
				t.Fatalf("error = %v, want AlreadyExists", err)
			}
			if f.clones != 0 || len(f.deletes) != 0 || len(f.creates) != 0 {
				t.Errorf("clones=%d deletes=%v creates=%v, want nothing touched", f.clones, f.deletes, f.creates)
			}
		})
	}
}

// The dataset can appear between the lookup and the clone (or be missed by the lookup).
// The clone's "already exists" means the earlier attempt's dataset, which must be kept.
func TestCreateVolumeFromSnapshotCloneEEXISTResumes(t *testing.T) {
	t.Parallel()
	f := newCloneRetryFake().withTarget("FILESYSTEM").withPresentation(ProtocolNFS)
	f.hiddenTarget = true
	f.cloneErr = errors.New("[EEXIST] dataset tank/csi/pvc-new already exists")
	req, snapshotID := cloneRetryRequest(ProtocolNFS, nil)

	resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	checkClonedVolume(t, resp, snapshotID)
	if len(f.deletes) != 0 {
		t.Errorf("clone EEXIST deleted %v", f.deletes)
	}
	if len(f.creates) != 0 {
		t.Errorf("create calls = %v, want the existing share reused", f.creates)
	}
}

// Promoted mode: once a promote has happened, the clone is the origin of the source's
// snapshot. A retry must not promote again (or fail), and must not try to delete it.
func TestCreateVolumeFromSnapshotPromotedRetry(t *testing.T) {
	t.Parallel()
	promoted := map[string]string{PromotedVolumesFromSnapshotsParam: VolumeContextValueTrue}

	t.Run("already promoted", func(t *testing.T) {
		t.Parallel()
		f := newCloneRetryFake().withTarget("FILESYSTEM").withPresentation(ProtocolNFS)
		f.target.Origin = map[string]interface{}{"value": ""}
		req, snapshotID := cloneRetryRequest(ProtocolNFS, promoted)

		resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
		if err != nil {
			t.Fatalf("retry failed: %v", err)
		}
		checkClonedVolume(t, resp, snapshotID)
		if f.promotes != 0 || f.clones != 0 || len(f.deletes) != 0 {
			t.Errorf("promotes=%d clones=%d deletes=%v, want none", f.promotes, f.clones, f.deletes)
		}
	})

	t.Run("cloned but not promoted yet", func(t *testing.T) {
		t.Parallel()
		f := newCloneRetryFake().withTarget("FILESYSTEM")
		f.target.Origin = map[string]interface{}{"value": cloneRetrySnap}
		req, snapshotID := cloneRetryRequest(ProtocolNFS, promoted)

		resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
		if err != nil {
			t.Fatalf("retry failed: %v", err)
		}
		checkClonedVolume(t, resp, snapshotID)
		if f.promotes != 1 || f.clones != 0 || len(f.deletes) != 0 {
			t.Errorf("promotes=%d clones=%d deletes=%v, want one promote and nothing else", f.promotes, f.clones, f.deletes)
		}
	})

	// The first attempt's promote went through on TrueNAS but its response was lost, so
	// the clone can no longer be deleted. The retry must complete the volume.
	t.Run("promote response lost", func(t *testing.T) {
		t.Parallel()
		f := newCloneRetryFake()
		f.promoteErr = errors.New("context deadline exceeded")
		f.deleteErr = errors.New("cannot destroy 'tank/csi/pvc-new': filesystem has dependent clones")
		s := NewControllerService(f.mock(), NewNodeRegistry(), "")
		req, snapshotID := cloneRetryRequest(ProtocolNFS, promoted)

		if _, err := s.createVolumeFromSnapshot(context.Background(), req, snapshotID); err == nil {
			t.Fatal("first attempt succeeded although its promote failed")
		}
		f.promoteErr = nil
		resp, err := s.createVolumeFromSnapshot(context.Background(), req, snapshotID)
		if err != nil {
			t.Fatalf("retry failed: %v", err)
		}
		checkClonedVolume(t, resp, snapshotID)
		if f.clones != 1 || f.promotes != 1 {
			t.Errorf("clones=%d promotes=%d, want 1 and 1 (the retry must reuse the promoted clone)", f.clones, f.promotes)
		}
	})
}

// Cleanup after a failed clone must not run on the request context: it has usually just
// expired, and that is why the clone failed.
func TestCreateVolumeFromSnapshotCleanupUsesDetachedContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newCloneRetryFake()
	m := f.mock()
	m.CloneSnapshotFunc = func(context.Context, tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
		cancel()
		return nil, errors.New("context deadline exceeded")
	}
	req, snapshotID := cloneRetryRequest(ProtocolNFS, nil)

	if _, err := NewControllerService(m, NewNodeRegistry(), "").createVolumeFromSnapshot(ctx, req, snapshotID); err == nil {
		t.Fatal("create succeeded although the clone failed")
	}
	if want := []string{cloneRetryTarget}; !reflect.DeepEqual(f.deletes, want) {
		t.Errorf("cleanup deleted %v, want %v", f.deletes, want)
	}
	if len(f.staleCtx) != 0 {
		t.Errorf("cleanup of %v ran on the canceled request context", f.staleCtx)
	}
}

// Clone setup must never delete a ZVOL an earlier attempt cloned, even when it fails.
func TestSetupNVMeOFVolumeFromCloneKeepsReusedZvol(t *testing.T) {
	t.Parallel()
	f := newNVMeOFRetryFake(nvmeStageSubsystem)
	f.namespaceFail = func() error { return errors.New("namespace create failed") }
	info := &cloneInfo{Mode: tnsapi.CloneModeCOW, SnapshotID: "snap", Reused: true}
	_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").setupNVMeOFVolumeFromClone(
		context.Background(), nvmeRetryRequest(), nvmeRetryZvolDataset(), "192.168.1.100", "", info)
	if err == nil {
		t.Fatal("clone setup succeeded although the namespace create failed")
	}
	if want := []string{"port"}; !reflect.DeepEqual(f.deletes, want) {
		t.Errorf("cleanup deleted %v, want %v (the reused ZVOL must be kept)", f.deletes, want)
	}
}
