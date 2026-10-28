package driver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

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

// If origin did not reveal that the clone is already promoted, PromoteDataset reports
// "not a clone"; that means there is nothing to do, not a failure.
func TestCreateVolumeFromSnapshotPromoteNothingToDo(t *testing.T) {
	t.Parallel()
	f := newCloneRetryFake().withTarget("FILESYSTEM").withPresentation(ProtocolNFS)
	f.promoteErr = &tnsapi.Error{ErrorName: "EINVAL", Reason: "Only cloned datasets can be promoted."}
	req, snapshotID := cloneRetryRequest(ProtocolNFS, map[string]string{PromotedVolumesFromSnapshotsParam: VolumeContextValueTrue})

	resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	checkClonedVolume(t, resp, snapshotID)
	if f.promotes != 1 || f.clones != 0 || len(f.deletes) != 0 {
		t.Errorf("promotes=%d clones=%d deletes=%v, want one promote attempt and nothing else", f.promotes, f.clones, f.deletes)
	}
}

// Detached (send/receive) mode: replication copies the source's properties except
// csi_volume_name, which only setup writes. A receive that TrueNAS finished after the
// request timed out leaves exactly that state; the retry must complete the volume.
func TestCreateVolumeFromSnapshotDetachedResumesReceivedDataset(t *testing.T) {
	t.Parallel()
	f := newCloneRetryFake().withTarget("FILESYSTEM")
	delete(f.props, tnsapi.PropertyCSIVolumeName)
	req, snapshotID := cloneRetryRequest(ProtocolNFS, map[string]string{DetachedVolumesFromSnapshotsParam: VolumeContextValueTrue})

	resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	checkClonedVolume(t, resp, snapshotID)
	// A new replication would be followed by a promote of the received dataset.
	if f.promotes != 0 || len(f.deletes) != 0 {
		t.Errorf("promotes=%d deletes=%v, want the received dataset reused without a new replication", f.promotes, f.deletes)
	}
	if got := f.props[tnsapi.PropertyCSIVolumeName]; got != cloneRetryName {
		t.Errorf("csi_volume_name = %q after resume, want %q", got, cloneRetryName)
	}
}

// detachedReplicationFake adds the replication side of TrueNAS to cloneRetryFake: the jobs
// core.get_jobs still knows for the clone target, and the calls the driver makes.
type detachedReplicationFake struct {
	*cloneRetryFake
	jobs             []tnsapi.ReplicationJobState
	waited           []int
	deletedSnapshots []string
	events           []string // "delete" and "replicate", in call order
	replications     int
}

func newDetachedReplicationFake(jobs ...tnsapi.ReplicationJobState) *detachedReplicationFake {
	return &detachedReplicationFake{cloneRetryFake: newCloneRetryFake(), jobs: jobs}
}

// withReceivedTarget gives the fake a target a receive made: managed_by is copied from the
// source, csi_volume_name is not.
func (f *detachedReplicationFake) withReceivedTarget() *detachedReplicationFake {
	f.withTarget("FILESYSTEM")
	delete(f.props, tnsapi.PropertyCSIVolumeName)
	return f
}

// receive makes the target the way a finished replication does. Callers hold f.mu.
func (f *detachedReplicationFake) receive() {
	f.target = &tnsapi.Dataset{ID: cloneRetryTarget, Name: cloneRetryTarget, Type: "FILESYSTEM", Mountpoint: cloneRetryMount}
	f.props[tnsapi.PropertyManagedBy] = tnsapi.ManagedByValue
}

func (f *detachedReplicationFake) mock() *MockAPIClientForSnapshots {
	m := f.cloneRetryFake.mock()
	deleteDataset := m.DeleteDatasetFunc
	m.DeleteDatasetFunc = func(ctx context.Context, id string) error {
		f.mu.Lock()
		f.events = append(f.events, "delete")
		f.mu.Unlock()
		return deleteDataset(ctx, id)
	}
	m.DeleteSnapshotFunc = func(_ context.Context, id string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletedSnapshots = append(f.deletedSnapshots, id)
		return nil
	}
	m.FindReplicationJobsForTargetFunc = func(_ context.Context, target string) ([]tnsapi.ReplicationJobState, error) {
		if target != cloneRetryTarget {
			return nil, nil
		}
		return f.jobs, nil
	}
	m.WaitForJobFunc = func(_ context.Context, jobID int, _ time.Duration) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.waited = append(f.waited, jobID)
		f.receive()
		return nil
	}
	m.RunOnetimeReplicationAndWaitFunc = func(_ context.Context, p tnsapi.ReplicationRunOnetimeParams, _ time.Duration) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.replications++
		f.events = append(f.events, "replicate")
		if p.TargetDataset == cloneRetryTarget {
			f.receive()
		}
		return nil
	}
	return m
}

func detachedRequest() (req *csi.CreateVolumeRequest, snapshotID string) {
	return cloneRetryRequest(ProtocolNFS, map[string]string{DetachedVolumesFromSnapshotsParam: VolumeContextValueTrue})
}

// TrueNAS keeps running a replication after the request that started it timed out. The
// retry must wait for that job, not start another one into the same target (which fails
// and restarts the copy, so a large volume never converges), and must not delete it.
func TestCreateVolumeFromSnapshotDetachedWaitsForRunningReplication(t *testing.T) {
	t.Parallel()
	f := newDetachedReplicationFake(
		tnsapi.ReplicationJobState{ID: 40, State: "FAILED"},
		tnsapi.ReplicationJobState{ID: 41, State: "RUNNING"},
	)
	req, snapshotID := detachedRequest()

	resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	checkClonedVolume(t, resp, snapshotID)
	if !reflect.DeepEqual(f.waited, []int{41}) || f.replications != 0 || len(f.deletes) != 0 {
		t.Errorf("waited=%v replications=%d deletes=%v, want a wait on job 41 and nothing else", f.waited, f.replications, f.deletes)
	}
	if f.promotes != 1 || !reflect.DeepEqual(f.deletedSnapshots, []string{cloneRetryTarget + "@snap1"}) {
		t.Errorf("promotes=%d deleted snapshots=%v, want the post-replication promote and snapshot cleanup", f.promotes, f.deletedSnapshots)
	}
	if got := f.props[tnsapi.PropertyCSIVolumeName]; got != cloneRetryName {
		t.Errorf("csi_volume_name = %q, want %q", got, cloneRetryName)
	}
}

// The replication finished after the request timed out: the retry continues with the steps
// that follow it instead of copying again.
func TestCreateVolumeFromSnapshotDetachedContinuesAfterSucceededReplication(t *testing.T) {
	t.Parallel()
	f := newDetachedReplicationFake(tnsapi.ReplicationJobState{ID: 41, State: "SUCCESS"}).withReceivedTarget()
	req, snapshotID := detachedRequest()

	resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	checkClonedVolume(t, resp, snapshotID)
	if f.replications != 0 || len(f.waited) != 0 || len(f.deletes) != 0 {
		t.Errorf("replications=%d waited=%v deletes=%v, want none", f.replications, f.waited, f.deletes)
	}
	if f.promotes != 1 || !reflect.DeepEqual(f.deletedSnapshots, []string{cloneRetryTarget + "@snap1"}) {
		t.Errorf("promotes=%d deleted snapshots=%v, want the post-replication promote and snapshot cleanup", f.promotes, f.deletedSnapshots)
	}
	if got := f.props[tnsapi.PropertyCSIVolumeName]; got != cloneRetryName {
		t.Errorf("csi_volume_name = %q, want %q", got, cloneRetryName)
	}
}

// Only a failed replication is discarded: its partial target is deleted (on a context the
// expired request cannot cancel) and the copy starts again.
func TestCreateVolumeFromSnapshotDetachedRestartsAfterFailedReplication(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"FAILED", "ABORTED"} {
		f := newDetachedReplicationFake(tnsapi.ReplicationJobState{ID: 41, State: state}).withReceivedTarget()
		req, snapshotID := detachedRequest()

		resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
		if err != nil {
			t.Fatalf("%s: retry failed: %v", state, err)
		}
		checkClonedVolume(t, resp, snapshotID)
		if !reflect.DeepEqual(f.events, []string{"delete", "replicate"}) || len(f.waited) != 0 {
			t.Errorf("%s: events=%v waited=%v, want the target deleted, then one new replication", state, f.events, f.waited)
		}
		if !reflect.DeepEqual(f.deletes, []string{cloneRetryTarget}) || len(f.staleCtx) != 0 {
			t.Errorf("%s: deletes=%v (on canceled ctx: %v), want %s deleted on a live context", state, f.deletes, f.staleCtx, cloneRetryTarget)
		}
	}
}

// When the request's deadline expires during the wait, the copy is still running on
// TrueNAS: report DeadlineExceeded and keep the target so the next retry picks the job up.
// This holds for a job this request started as well as one it found.
func TestCreateVolumeFromSnapshotDetachedDeadlineKeepsTarget(t *testing.T) {
	t.Parallel()
	waitUntilDone := func(ctx context.Context) error {
		<-ctx.Done()
		return fmt.Errorf("context canceled while waiting for job 41: %w", ctx.Err())
	}
	for _, running := range []bool{true, false} {
		var f *detachedReplicationFake
		if running {
			f = newDetachedReplicationFake(tnsapi.ReplicationJobState{ID: 41, State: "RUNNING"}).withReceivedTarget()
		} else {
			f = newDetachedReplicationFake()
		}
		m := f.mock()
		m.WaitForJobFunc = func(ctx context.Context, _ int, _ time.Duration) error { return waitUntilDone(ctx) }
		m.RunOnetimeReplicationAndWaitFunc = func(ctx context.Context, _ tnsapi.ReplicationRunOnetimeParams, _ time.Duration) error {
			f.mu.Lock()
			f.replications++
			f.receive() // the receive is under way; its target must survive
			f.mu.Unlock()
			return waitUntilDone(ctx)
		}
		req, snapshotID := detachedRequest()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)

		_, err := NewControllerService(m, NewNodeRegistry(), "").createVolumeFromSnapshot(ctx, req, snapshotID)
		cancel()
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("running job=%v: error = %v, want DeadlineExceeded", running, err)
		}
		if len(f.deletes) != 0 {
			t.Errorf("running job=%v: deletes=%v, want the target kept", running, f.deletes)
		}
		if running && f.replications != 0 {
			t.Errorf("replications=%d, want none while a job is running", f.replications)
		}
	}
}

// Outside detached mode the clone call writes csi_volume_name, so a dataset without it is
// not this volume's even when managed_by says tns-csi.
func TestCreateVolumeFromSnapshotUnnamedTargetIsForeignOutsideDetachedMode(t *testing.T) {
	t.Parallel()
	f := newCloneRetryFake().withTarget("FILESYSTEM")
	delete(f.props, tnsapi.PropertyCSIVolumeName)
	req, snapshotID := cloneRetryRequest(ProtocolNFS, nil)

	_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createVolumeFromSnapshot(context.Background(), req, snapshotID)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("error = %v, want AlreadyExists", err)
	}
	if f.clones != 0 || len(f.deletes) != 0 {
		t.Errorf("clones=%d deletes=%v, want nothing touched", f.clones, f.deletes)
	}
}

// An enabled share at the path of a FRESH clone was left by something else (e.g. an older
// attempt whose dataset is gone). The clone still needs its ACL conversion.
func TestSetupSMBVolumeFromCloneConvertsACLsUnlessReused(t *testing.T) {
	t.Parallel()
	for _, reused := range []bool{false, true} {
		f := newCloneRetryFake().withTarget("FILESYSTEM").withPresentation(ProtocolSMB)
		m := f.mock()
		aclUpdates := 0
		m.UpdateDatasetFunc = func(_ context.Context, _ string, p tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
			if p.Acltype != "" {
				aclUpdates++
			}
			return f.target, nil
		}
		req, _ := cloneRetryRequest(ProtocolSMB, nil)
		info := &cloneInfo{Mode: tnsapi.CloneModeCOW, SnapshotID: "snap", Reused: reused}

		if _, err := NewControllerService(m, NewNodeRegistry(), "").setupSMBVolumeFromClone(context.Background(), req, f.target, "truenas.example", info); err != nil {
			t.Fatalf("reused=%v: setup failed: %v", reused, err)
		}
		want := 1
		if reused {
			want = 0
		}
		if aclUpdates != want {
			t.Errorf("reused=%v: ACL conversions = %d, want %d", reused, aclUpdates, want)
		}
	}
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
