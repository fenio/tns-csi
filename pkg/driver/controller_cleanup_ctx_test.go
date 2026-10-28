package driver

import (
	"context"
	"errors"
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

// The dataset made for a volume is removed after the share create fails, even though the
// request context that caused the failure is already done.
func TestShareCreateFailureCleansUpDatasetOnDetachedContext(t *testing.T) {
	dataset := &tnsapi.Dataset{ID: "tank/csi/pvc-1", Name: "tank/csi/pvc-1", Mountpoint: "/mnt/tank/csi/pvc-1"}
	for name, call := range map[string]func(*ControllerService, context.Context) error{
		"nfs": func(s *ControllerService, ctx context.Context) error {
			_, err := s.createNFSShareForDataset(ctx, dataset, &nfsVolumeParams{volumeName: "pvc-1"}, true, metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create"))
			return err
		},
		"smb": func(s *ControllerService, ctx context.Context) error {
			_, err := s.createSMBShareForDataset(ctx, dataset, &smbVolumeParams{volumeName: "pvc-1"}, true, metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "create"))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			deleted := false
			mock := &MockAPIClientForSnapshots{
				CreateNFSShareFunc: func(context.Context, tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					return nil, errors.New("deadline exceeded")
				},
				CreateSMBShareFunc: func(context.Context, tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
					return nil, errors.New("deadline exceeded")
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
			if err := call(s, cancelledContext()); err == nil {
				t.Fatal("expected the share create failure to be returned")
			}
			if !deleted {
				t.Error("dataset was not cleaned up")
			}
		})
	}
}
