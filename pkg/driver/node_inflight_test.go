package driver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/cmdexec"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func stillRunning(done <-chan struct{}) error {
	return &cmdexec.StillRunningError{Name: "mkfs.ext4", Done: done, Err: context.DeadlineExceeded}
}

func waitNotBusy(t *testing.T, s *NodeService, volumeID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.checkNoLeftRunningOp(volumeID) == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("volume %s still reported busy after its command exited", volumeID)
}

func TestLeftRunningRegistersUnderTheRequestVolume(t *testing.T) {
	s := &NodeService{}
	done := make(chan struct{})
	ctx := withVolumeOp(context.Background(), "vol-1")
	err := s.leftRunning(ctx, "format of /dev/sdb", stillRunning(done))
	if status.Code(err) != codes.Aborted {
		t.Fatalf("leftRunning() = %v; want Aborted", err)
	}
	if busy := s.checkNoLeftRunningOp("vol-1"); status.Code(busy) != codes.Aborted {
		t.Fatalf("checkNoLeftRunningOp() = %v, want Aborted while the command runs", busy)
	}
	if other := s.checkNoLeftRunningOp("vol-2"); other != nil {
		t.Fatalf("another volume reported busy: %v", other)
	}
	close(done)
	waitNotBusy(t, s, "vol-1")
}

func TestLeftRunningIgnoresOtherErrors(t *testing.T) {
	s := &NodeService{}
	if err := s.leftRunning(withVolumeOp(context.Background(), "v"), "x", context.Canceled); err != nil {
		t.Fatal("leftRunning() claimed a plain error")
	}
}

// mkfs must not be killed part way (a half-written superblock); a format still running when
// the RPC ends is left to finish and the volume reports Aborted until it does.
func TestHandleDeviceFormattingLeavesSlowFormatRunning(t *testing.T) {
	done := make(chan struct{})
	s := &NodeService{
		needsFormatFn: func(context.Context, string, bool) (bool, error) { return true, nil },
		formatDeviceFn: func(context.Context, string, string, string, []string) error {
			return stillRunning(done)
		},
	}
	ctx := withVolumeOp(context.Background(), "vol-1")
	_, err := s.handleDeviceFormatting(ctx, "vol-1", "/dev/sdb", filesystemSpec{fsType: fsTypeExt4}, "tank/vol-1", "iqn", false)
	if status.Code(err) != codes.Aborted {
		t.Fatalf("handleDeviceFormatting() = %v, want Aborted", err)
	}
	if status.Code(s.checkNoLeftRunningOp("vol-1")) != codes.Aborted {
		t.Fatal("format left running was not registered")
	}
	close(done)
	waitNotBusy(t, s, "vol-1")
}

func TestRunFilesystemPreenLeftRunningIsAbortedNotInternal(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	s := &NodeService{runFilesystemCheckFn: func(context.Context, string) ([]byte, error) { return nil, stillRunning(done) }}
	err := s.runFilesystemPreen(withVolumeOp(context.Background(), "vol-1"), "/dev/sdb", fsTypeExt4)
	if status.Code(err) != codes.Aborted {
		t.Fatalf("runFilesystemPreen() = %v, want Aborted", err)
	}
}

// Unstaging under a running mkfs would log out or disconnect the device it is writing to.
func TestNodeUnstageVolumeRefusesWhileCommandLeftRunning(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	s := &NodeService{}
	_ = s.leftRunning(withVolumeOp(context.Background(), "vol-1"), "format of /dev/sdb", stillRunning(done))
	_, err := s.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId: "vol-1", StagingTargetPath: filepath.Join(t.TempDir(), "globalmount"),
	})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("NodeUnstageVolume() = %v, want Aborted", err)
	}
}

func TestNodeStageVolumeRefusesWhileCommandLeftRunning(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	s := &NodeService{}
	_ = s.leftRunning(withVolumeOp(context.Background(), "vol-1"), "format of /dev/sdb", stillRunning(done))
	_, err := s.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId: "vol-1", StagingTargetPath: filepath.Join(t.TempDir(), "globalmount"),
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
		VolumeContext:    map[string]string{VolumeContextKeyProtocol: ProtocolISCSI, VolumeContextKeyISCSIIQN: testISCSIIQN, "server": "192.0.2.10"},
	})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("NodeStageVolume() = %v, want Aborted", err)
	}
}
