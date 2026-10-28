package driver

import (
	"context"
	"errors"
	"testing"
	"time"

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
