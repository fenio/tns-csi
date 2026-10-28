package driver

import (
	"context"
	"fmt"
	"testing"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// cancelledContext returns a context like a request whose deadline has just expired.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestCleanupContextIgnoresRequestCancellation(t *testing.T) {
	ctx, cancel := cleanupContext(cancelledContext())
	defer cancel()
	if ctx.Err() != nil {
		t.Fatalf("cleanup context is already done: %v", ctx.Err())
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Error("cleanup context has no deadline")
	}
}

// A share create that middlewared definitely rejected leaves a dataset nobody uses, and it
// is removed on a detached context even though the request context is already done. When
// the share may have been created (uncertain outcome, or the request context ran out
// before a response arrived) the dataset is kept: deleting it would orphan the share, and
// the retry converges through the existing-volume path, which finds the share by path.
func TestShareCreateFailureCleansUpDatasetOnDetachedContext(t *testing.T) {
	dataset := &tnsapi.Dataset{ID: "tank/csi/pvc-1", Name: "tank/csi/pvc-1", Mountpoint: "/mnt/tank/csi/pvc-1"}
	calls := map[string]func(*ControllerService, context.Context) error{
		"nfs": func(s *ControllerService, ctx context.Context) error {
			_, err := s.createNFSShareForDataset(ctx, dataset, &nfsVolumeParams{volumeName: "pvc-1"}, true, metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create"))
			return err
		},
		"smb": func(s *ControllerService, ctx context.Context) error {
			_, err := s.createSMBShareForDataset(ctx, dataset, &smbVolumeParams{volumeName: "pvc-1"}, true, metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "create"))
			return err
		},
	}
	cases := []struct {
		ctx        func() context.Context
		shareErr   error
		name       string
		wantDelete bool
	}{
		{name: "definite failure with expired ctx", ctx: cancelledContext, shareErr: &tnsapi.Error{ErrorName: "EINVAL", Reason: "path is invalid"}, wantDelete: true},
		{name: "uncertain outcome", ctx: context.Background, shareErr: fmt.Errorf("%w (sharing.create): connection reset", tnsapi.ErrUncertainOutcome), wantDelete: false},
		{name: "no response before ctx expired", ctx: cancelledContext, shareErr: context.Canceled, wantDelete: false},
	}
	for proto, call := range calls {
		for _, tc := range cases {
			t.Run(proto+"/"+tc.name, func(t *testing.T) {
				deleted := false
				mock := &MockAPIClientForSnapshots{
					CreateNFSShareFunc: func(context.Context, tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
						return nil, tc.shareErr
					},
					CreateSMBShareFunc: func(context.Context, tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
						return nil, tc.shareErr
					},
					DeleteDatasetFunc: func(ctx context.Context, id string) error {
						deleted = true
						if ctx.Err() != nil {
							t.Errorf("DeleteDataset(%s) got a done context: %v", id, ctx.Err())
						}
						return nil
					},
				}
				s := NewControllerService(mock, NewNodeRegistry(), "")
				if err := call(s, tc.ctx()); err == nil {
					t.Fatal("expected the share create failure to be returned")
				}
				if deleted != tc.wantDelete {
					t.Errorf("dataset deleted = %v, want %v", deleted, tc.wantDelete)
				}
			})
		}
	}
}
