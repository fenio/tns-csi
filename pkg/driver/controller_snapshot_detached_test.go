package driver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	detachedSnapParent = "tank/csi-detached-snapshots"
	detachedSnapSource = "tank/csi/pvc-src"
	detachedSnapName   = "snapshot-1"
	detachedSnapTarget = detachedSnapParent + "/" + detachedSnapName
)

// detachedSnapshotMock is a TrueNAS where the detached-snapshot parent exists, the target
// does not, and the replication into the target ends with whatever replicate returns.
// Deletes are recorded.
func detachedSnapshotMock(replicate func(ctx context.Context) error, deletedDatasets, deletedSnapshots *[]string) *MockAPIClientForSnapshots {
	return &MockAPIClientForSnapshots{
		GetDatasetFunc: func(_ context.Context, id string) (*tnsapi.Dataset, error) {
			if id == detachedSnapParent {
				return &tnsapi.Dataset{ID: id, Name: id}, nil
			}
			return nil, tnsapi.ErrDatasetNotFound
		},
		CreateSnapshotFunc: func(_ context.Context, p tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
			return &tnsapi.Snapshot{ID: p.Dataset + "@" + p.Name, Name: p.Name, Dataset: p.Dataset}, nil
		},
		RunOnetimeReplicationAndWaitFunc: func(ctx context.Context, _ tnsapi.ReplicationRunOnetimeParams, _ time.Duration) error {
			return replicate(ctx)
		},
		DeleteDatasetFunc: func(_ context.Context, id string) error {
			*deletedDatasets = append(*deletedDatasets, id)
			return nil
		},
		DeleteSnapshotFunc: func(_ context.Context, id string) error {
			*deletedSnapshots = append(*deletedSnapshots, id)
			return nil
		},
	}
}

func runDetachedSnapshot(ctx context.Context, s *ControllerService) error {
	_, err := s.createDetachedSnapshot(ctx, metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "snapshot"),
		detachedSnapName, "pvc-src", detachedSnapSource, ProtocolNFS, "tank", detachedSnapParent, 1<<30)
	return err
}

// When the request context runs out while the replication is still running on TrueNAS, the
// partial target and the source snapshot being sent are both left alone: deleting either
// would break the running copy and make every retry start it again.
func TestCreateDetachedSnapshotLeavesRunningReplicationAlone(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for name, tc := range map[string]struct {
		ctx      context.Context
		wantCode codes.Code
	}{
		"deadline exceeded": {ctx: expired, wantCode: codes.DeadlineExceeded},
		"canceled":          {ctx: cancelledContext(), wantCode: codes.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			var deletedDatasets, deletedSnapshots []string
			replicate := func(ctx context.Context) error {
				return errors.Join(errors.New("waiting for replication job 42"), ctx.Err())
			}
			s := NewControllerService(detachedSnapshotMock(replicate, &deletedDatasets, &deletedSnapshots), NewNodeRegistry(), "")
			err := runDetachedSnapshot(tc.ctx, s)
			if status.Code(err) != tc.wantCode {
				t.Fatalf("createDetachedSnapshot() error = %v, want code %v", err, tc.wantCode)
			}
			if len(deletedDatasets) != 0 {
				t.Errorf("DeleteDataset called for %v, want the target left to the running replication", deletedDatasets)
			}
			if len(deletedSnapshots) != 0 {
				t.Errorf("DeleteSnapshot called for %v, want the temp snapshot kept while it is sent", deletedSnapshots)
			}
		})
	}
}

// A replication that definitely failed still has its partial target and temp snapshot removed.
func TestCreateDetachedSnapshotCleansUpFailedReplication(t *testing.T) {
	var deletedDatasets, deletedSnapshots []string
	replicate := func(context.Context) error { return errors.New("replication job FAILED: cannot receive") }
	s := NewControllerService(detachedSnapshotMock(replicate, &deletedDatasets, &deletedSnapshots), NewNodeRegistry(), "")
	if err := runDetachedSnapshot(context.Background(), s); status.Code(err) != codes.Internal {
		t.Fatalf("createDetachedSnapshot() error = %v, want Internal", err)
	}
	if len(deletedDatasets) != 1 || deletedDatasets[0] != detachedSnapTarget {
		t.Errorf("DeleteDataset calls = %v, want [%s]", deletedDatasets, detachedSnapTarget)
	}
	if len(deletedSnapshots) != 1 {
		t.Errorf("DeleteSnapshot calls = %v, want the temp snapshot removed", deletedSnapshots)
	}
}

// detachedSnapTemp is the temporary snapshot a CreateSnapshot for detachedSnapName sends.
const detachedSnapTemp = "csi-detached-temp-" + detachedSnapName

// detachedSnapshotRetryFake is a stateful TrueNAS for retried detached CreateSnapshot calls:
// the replication jobs core.get_jobs still knows for the target, the target dataset and its
// user properties, and the calls the driver makes.
type detachedSnapshotRetryFake struct {
	target           *tnsapi.Dataset // nil: the target does not exist
	props            map[string]string
	jobs             []tnsapi.ReplicationJobState
	waited           []int
	events           []string // "delete" and "replicate", in call order
	deletes          []string
	staleCtx         []string // deletes that ran on an already-canceled context
	promotes         []string
	deletedSnapshots []string
	createdSnapshots []string
	propWrites       int
	replications     int
	mu               sync.Mutex
}

func newDetachedSnapshotRetryFake(jobs ...tnsapi.ReplicationJobState) *detachedSnapshotRetryFake {
	return &detachedSnapshotRetryFake{props: map[string]string{}, jobs: jobs}
}

// receive makes the target the way a finished replication does: managed_by is copied from
// the source volume, the snapshot's own properties are not there yet. Callers hold f.mu.
func (f *detachedSnapshotRetryFake) receive() {
	f.target = &tnsapi.Dataset{ID: detachedSnapTarget, Name: detachedSnapTarget, Type: "FILESYSTEM"}
	f.props[tnsapi.PropertyManagedBy] = tnsapi.ManagedByValue
}

// withReceivedTarget gives the fake a target an earlier attempt's replication made.
func (f *detachedSnapshotRetryFake) withReceivedTarget() *detachedSnapshotRetryFake {
	f.receive()
	return f
}

func (f *detachedSnapshotRetryFake) mock() *MockAPIClientForSnapshots {
	return &MockAPIClientForSnapshots{
		GetDatasetFunc: func(_ context.Context, id string) (*tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch {
			case id == detachedSnapParent:
				return &tnsapi.Dataset{ID: id, Name: id}, nil
			case id == detachedSnapTarget && f.target != nil:
				ds := *f.target
				return &ds, nil
			}
			return nil, tnsapi.ErrDatasetNotFound
		},
		GetDatasetPropertiesFunc: func(_ context.Context, id string, names []string) (map[string]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id != detachedSnapTarget || f.target == nil {
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
		SetDatasetPropertiesFunc: func(_ context.Context, id string, props map[string]string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id == detachedSnapTarget {
				f.propWrites++
				for k, v := range props {
					f.props[k] = v
				}
			}
			return nil
		},
		CreateSnapshotFunc: func(_ context.Context, p tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.createdSnapshots = append(f.createdSnapshots, p.Dataset+"@"+p.Name)
			return &tnsapi.Snapshot{ID: p.Dataset + "@" + p.Name, Name: p.Name, Dataset: p.Dataset}, nil
		},
		DeleteSnapshotFunc: func(_ context.Context, id string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deletedSnapshots = append(f.deletedSnapshots, id)
			return nil
		},
		PromoteDatasetFunc: func(_ context.Context, id string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.promotes = append(f.promotes, id)
			return errors.New("[EBADMSG] Only cloned datasets can be promoted")
		},
		DeleteDatasetFunc: func(ctx context.Context, id string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.events = append(f.events, "delete")
			f.deletes = append(f.deletes, id)
			if ctx.Err() != nil {
				f.staleCtx = append(f.staleCtx, id)
				return ctx.Err()
			}
			if id == detachedSnapTarget {
				f.target = nil
				f.props = map[string]string{}
			}
			return nil
		},
		FindReplicationJobsForTargetFunc: func(_ context.Context, target string) ([]tnsapi.ReplicationJobState, error) {
			if target != detachedSnapTarget {
				return nil, nil
			}
			return f.jobs, nil
		},
		WaitForJobFunc: func(_ context.Context, jobID int, _ time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.waited = append(f.waited, jobID)
			f.receive()
			return nil
		},
		RunOnetimeReplicationAndWaitFunc: func(_ context.Context, p tnsapi.ReplicationRunOnetimeParams, _ time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.replications++
			f.events = append(f.events, "replicate")
			if p.TargetDataset == detachedSnapTarget {
				f.receive()
			}
			return nil
		},
	}
}

func runDetachedSnapshotResp(ctx context.Context, m *MockAPIClientForSnapshots) (*csi.CreateSnapshotResponse, error) {
	return NewControllerService(m, NewNodeRegistry(), "").createDetachedSnapshot(ctx, metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "snapshot"),
		detachedSnapName, "pvc-src", detachedSnapSource, ProtocolNFS, "tank", detachedSnapParent, 1<<30)
}

// checkFinishedDetachedSnapshot checks a call reported the snapshot ready only with the
// post-replication steps run: promote, both temp snapshots removed, CSI properties written.
func checkFinishedDetachedSnapshot(t *testing.T, f *detachedSnapshotRetryFake, resp *csi.CreateSnapshotResponse) {
	t.Helper()
	if !resp.GetSnapshot().GetReadyToUse() {
		t.Errorf("ReadyToUse = false, want true")
	}
	if !reflect.DeepEqual(f.promotes, []string{detachedSnapTarget}) {
		t.Errorf("promotes = %v, want [%s]", f.promotes, detachedSnapTarget)
	}
	for _, snap := range []string{detachedSnapTarget + "@" + detachedSnapTemp, detachedSnapSource + "@" + detachedSnapTemp} {
		if !slices.Contains(f.deletedSnapshots, snap) {
			t.Errorf("deleted snapshots = %v, want %s removed", f.deletedSnapshots, snap)
		}
	}
	if f.props[tnsapi.PropertySnapshotID] != detachedSnapName || f.props[tnsapi.PropertyDetachedSnapshot] != VolumeContextValueTrue {
		t.Errorf("target properties = %v, want the snapshot's CSI properties written", f.props)
	}
}

// TrueNAS keeps running the replication after the CreateSnapshot that started it timed
// out. The retry must wait for that job rather than report the partial copy as ready, and
// must neither start another replication nor delete anything.
func TestCreateDetachedSnapshotRetryWaitsForRunningReplication(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"RUNNING", "WAITING"} {
		f := newDetachedSnapshotRetryFake(
			tnsapi.ReplicationJobState{ID: 40, State: "FAILED"},
			tnsapi.ReplicationJobState{ID: 41, State: state},
		).withReceivedTarget()
		m := f.mock()
		wait := m.WaitForJobFunc
		m.WaitForJobFunc = func(ctx context.Context, jobID int, d time.Duration) error {
			f.mu.Lock()
			written := f.propWrites
			f.mu.Unlock()
			if written != 0 {
				t.Errorf("%s: snapshot properties written before the job finished", state)
			}
			return wait(ctx, jobID, d)
		}

		resp, err := runDetachedSnapshotResp(context.Background(), m)
		if err != nil {
			t.Fatalf("%s: retry failed: %v", state, err)
		}
		if !reflect.DeepEqual(f.waited, []int{41}) || f.replications != 0 || len(f.deletes) != 0 || len(f.createdSnapshots) != 0 {
			t.Errorf("%s: waited=%v replications=%d deletes=%v created=%v, want only a wait on job 41",
				state, f.waited, f.replications, f.deletes, f.createdSnapshots)
		}
		checkFinishedDetachedSnapshot(t, f, resp)
	}
}

// The replication finished after the request timed out (or its job record is gone and the
// target holds a complete receive): the retry runs the steps that follow it instead of
// copying again.
func TestCreateDetachedSnapshotRetryFinishesSucceededReplication(t *testing.T) {
	t.Parallel()
	for name, jobs := range map[string][]tnsapi.ReplicationJobState{
		"job succeeded": {{ID: 41, State: "SUCCESS"}},
		"no job known":  nil,
	} {
		f := newDetachedSnapshotRetryFake(jobs...).withReceivedTarget()

		resp, err := runDetachedSnapshotResp(context.Background(), f.mock())
		if err != nil {
			t.Fatalf("%s: retry failed: %v", name, err)
		}
		if f.replications != 0 || len(f.waited) != 0 || len(f.deletes) != 0 || len(f.createdSnapshots) != 0 {
			t.Errorf("%s: replications=%d waited=%v deletes=%v created=%v, want none",
				name, f.replications, f.waited, f.deletes, f.createdSnapshots)
		}
		checkFinishedDetachedSnapshot(t, f, resp)
	}
}

// A failed or aborted replication, or a target still holding a receive_resume_token, is
// discarded and the copy starts again in the same call.
func TestCreateDetachedSnapshotRetryRestartsIncompleteReplication(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		waitError error
		name      string
		jobs      []tnsapi.ReplicationJobState
		token     bool
		unmarked  bool
	}{
		{name: "failed", jobs: []tnsapi.ReplicationJobState{{ID: 41, State: "FAILED", Error: "boom"}}},
		{name: "aborted", jobs: []tnsapi.ReplicationJobState{{ID: 41, State: "ABORTED"}}},
		{name: "failed without managed_by", jobs: []tnsapi.ReplicationJobState{{ID: 41, State: "FAILED"}}, unmarked: true},
		{name: "failed while waited for", jobs: []tnsapi.ReplicationJobState{{ID: 41, State: "RUNNING"}}, waitError: tnsapi.ErrJobFailed},
		{name: "resume token, no job", token: true},
		{name: "resume token, no job, without managed_by", token: true, unmarked: true},
	} {
		f := newDetachedSnapshotRetryFake(tc.jobs...).withReceivedTarget()
		if tc.unmarked {
			delete(f.props, tnsapi.PropertyManagedBy)
		}
		if tc.token {
			f.target.ReceiveResumeToken = map[string]interface{}{"value": "1-e604ea4bf-e0-789c63a2"}
		}
		m := f.mock()
		if tc.waitError != nil {
			waitError := tc.waitError
			m.WaitForJobFunc = func(context.Context, int, time.Duration) error {
				return fmt.Errorf("job 41: %w", waitError)
			}
		}

		resp, err := runDetachedSnapshotResp(context.Background(), m)
		if err != nil {
			t.Fatalf("%s: retry failed: %v", tc.name, err)
		}
		if !reflect.DeepEqual(f.events, []string{"delete", "replicate"}) {
			t.Errorf("%s: events=%v, want the target deleted, then one new replication", tc.name, f.events)
		}
		if !reflect.DeepEqual(f.deletes, []string{detachedSnapTarget}) || len(f.staleCtx) != 0 {
			t.Errorf("%s: deletes=%v (on canceled ctx: %v), want %s deleted on a live context", tc.name, f.deletes, f.staleCtx, detachedSnapTarget)
		}
		checkFinishedDetachedSnapshot(t, f, resp)
	}
}

// The discard of a failed receive runs on cleanupContext: a request whose context ends just
// before it still deletes the target.
func TestCreateDetachedSnapshotRetryDiscardUsesCleanupContext(t *testing.T) {
	t.Parallel()
	f := newDetachedSnapshotRetryFake(tnsapi.ReplicationJobState{ID: 41, State: "FAILED"}).withReceivedTarget()
	m := f.mock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The ownership check reads the target's properties right before the delete.
	getProps := m.GetDatasetPropertiesFunc
	m.GetDatasetPropertiesFunc = func(c context.Context, id string, names []string) (map[string]string, error) {
		defer cancel()
		return getProps(c, id, names)
	}

	_, _ = runDetachedSnapshotResp(ctx, m)
	if !reflect.DeepEqual(f.deletes, []string{detachedSnapTarget}) || len(f.staleCtx) != 0 {
		t.Errorf("deletes=%v (on canceled ctx: %v), want %s deleted on a live context", f.deletes, f.staleCtx, detachedSnapTarget)
	}
}

// When the request context runs out while waiting for the earlier attempt's job, the copy
// is still running: report the context error and delete nothing.
func TestCreateDetachedSnapshotRetryDeadlineWhileWaitingKeepsTarget(t *testing.T) {
	t.Parallel()
	f := newDetachedSnapshotRetryFake(tnsapi.ReplicationJobState{ID: 41, State: "RUNNING"}).withReceivedTarget()
	m := f.mock()
	m.WaitForJobFunc = func(ctx context.Context, _ int, _ time.Duration) error {
		<-ctx.Done()
		return fmt.Errorf("waiting for job 41: %w", ctx.Err())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := runDetachedSnapshotResp(ctx, m)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v (resp %v), want DeadlineExceeded", err, resp)
	}
	if len(f.deletes) != 0 || len(f.deletedSnapshots) != 0 || f.replications != 0 || len(f.createdSnapshots) != 0 || f.propWrites != 0 {
		t.Errorf("deletes=%v deleted snapshots=%v replications=%d created=%v propWrites=%d, want nothing touched",
			f.deletes, f.deletedSnapshots, f.replications, f.createdSnapshots, f.propWrites)
	}
}

// A dataset at the target path that belongs to someone else is never deleted, finished or
// reported as this snapshot, whatever the job record says.
func TestCreateDetachedSnapshotRetryKeepsForeignTarget(t *testing.T) {
	t.Parallel()
	for _, owner := range []struct{ key, value string }{
		{tnsapi.PropertyManagedBy, "someone-else"},
		{tnsapi.PropertySnapshotID, "snapshot-other"},
		{tnsapi.PropertyCSIVolumeName, "pvc-other"},
	} {
		for _, state := range []string{"FAILED", "SUCCESS", "none", "token"} {
			var f *detachedSnapshotRetryFake
			if state == "none" || state == "token" {
				f = newDetachedSnapshotRetryFake().withReceivedTarget()
			} else {
				f = newDetachedSnapshotRetryFake(tnsapi.ReplicationJobState{ID: 41, State: state}).withReceivedTarget()
			}
			if state == "token" {
				f.target.ReceiveResumeToken = map[string]interface{}{"value": "1-abc"}
			}
			f.props[owner.key] = owner.value

			_, err := runDetachedSnapshotResp(context.Background(), f.mock())
			if status.Code(err) != codes.AlreadyExists {
				t.Fatalf("%s=%s, job %s: error = %v, want AlreadyExists", owner.key, owner.value, state, err)
			}
			if len(f.deletes) != 0 || f.replications != 0 || len(f.promotes) != 0 || len(f.deletedSnapshots) != 0 ||
				len(f.createdSnapshots) != 0 || f.propWrites != 0 {

				t.Errorf("%s=%s, job %s: deletes=%v replications=%d promotes=%v deleted snapshots=%v created=%v propWrites=%d, want nothing touched",
					owner.key, owner.value, state, f.deletes, f.replications, f.promotes, f.deletedSnapshots, f.createdSnapshots, f.propWrites)
			}
		}
	}
}

// A failed job lookup is transient: the snapshotter should retry, and nothing is started.
func TestCreateDetachedSnapshotRetryJobLookupFailureIsUnavailable(t *testing.T) {
	t.Parallel()
	f := newDetachedSnapshotRetryFake().withReceivedTarget()
	m := f.mock()
	m.FindReplicationJobsForTargetFunc = func(context.Context, string) ([]tnsapi.ReplicationJobState, error) {
		return nil, errors.New("connection reset")
	}

	_, err := runDetachedSnapshotResp(context.Background(), m)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error = %v, want Unavailable", err)
	}
	if f.replications != 0 || len(f.deletes) != 0 || len(f.createdSnapshots) != 0 || len(f.promotes) != 0 || f.propWrites != 0 {
		t.Errorf("replications=%d deletes=%v created=%v promotes=%v propWrites=%d, want none",
			f.replications, f.deletes, f.createdSnapshots, f.promotes, f.propWrites)
	}
}

// A first attempt (no job, no target) creates the temp snapshot, replicates, finishes and
// cleans up as before.
func TestCreateDetachedSnapshotFirstAttempt(t *testing.T) {
	t.Parallel()
	f := newDetachedSnapshotRetryFake()

	resp, err := runDetachedSnapshotResp(context.Background(), f.mock())
	if err != nil {
		t.Fatalf("createDetachedSnapshot() failed: %v", err)
	}
	if f.replications != 1 || len(f.deletes) != 0 || !reflect.DeepEqual(f.createdSnapshots, []string{detachedSnapSource + "@" + detachedSnapTemp}) {
		t.Errorf("replications=%d deletes=%v created=%v, want one replication of a new temp snapshot", f.replications, f.deletes, f.createdSnapshots)
	}
	checkFinishedDetachedSnapshot(t, f, resp)
}
