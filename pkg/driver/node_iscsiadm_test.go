package driver

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/cmdexec"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestISCSIAdmNeedsDBLock(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"-m", "discovery", "-t", "sendtargets", "-p", "a:3260"}, true},
		{[]string{"--mode", "discoverydb", "-t", "st"}, true},
		{[]string{"--mode=node", "-P", "0"}, true},
		{[]string{"-m", "node", "-T", "iqn", "-p", "a", "--login"}, true},
		{[]string{"-m", "iface"}, true},
		{[]string{"-m", "session"}, false},
		{[]string{"-m", "session", "-P", "3"}, false},
		{[]string{"-m", "session", "-r", "4", "--logout"}, false},
		{[]string{"--version"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := iscsiadmNeedsDBLock(tt.args); got != tt.want {
			t.Errorf("iscsiadmNeedsDBLock(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// Concurrent NodeStage calls must not run iscsiadm against the node database in parallel:
// they only queue on open-iscsi's lock file and get killed for waiting (#336).
func TestRunISCSIAdmSerializesDatabaseOperations(t *testing.T) {
	service := &NodeService{}
	var active, maxActive atomic.Int32
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return nil, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "node", "-P", "0"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent database iscsiadm calls = %d, want 1", got)
	}
}

func TestRunISCSIAdmSessionQueriesBypassTheGate(t *testing.T) {
	service := &NodeService{}
	release := make(chan struct{})
	entered := make(chan struct{})
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "discovery") {
			close(entered)
			<-release
		}
		return nil, nil
	}
	go func() {
		_, _ = service.runISCSIAdm(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	}()
	<-entered
	done := make(chan error, 1)
	go func() {
		_, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "session", "-P", "3")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session query waited behind a database operation")
	}
	close(release)
}

// The per-call budget must start once the caller owns the gate, not while it queues.
func TestRunISCSIAdmTimeoutStartsAfterAcquiringGate(t *testing.T) {
	service := &NodeService{}
	release := make(chan struct{})
	entered := make(chan struct{})
	var remaining time.Duration
	service.runISCSIAdmFn = func(ctx context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "discovery") {
			close(entered)
			<-release
			return nil, nil
		}
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)
		return nil, nil
	}
	go func() {
		_, _ = service.runISCSIAdm(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	}()
	<-entered
	go func() { time.Sleep(300 * time.Millisecond); close(release) }()
	if _, err := service.runISCSIAdm(context.Background(), 200*time.Millisecond, "-m", "node", "-P", "0"); err != nil {
		t.Fatal(err)
	}
	if remaining < 100*time.Millisecond {
		t.Fatalf("budget left when the command started = %v; queueing consumed the per-call timeout", remaining)
	}
}

// A command left running past its deadline still holds open-iscsi's lock: the next caller
// must wait for it to exit instead of racing it.
func TestRunISCSIAdmHoldsGateUntilLeftRunningCommandExits(t *testing.T) {
	service := &NodeService{}
	exited := make(chan struct{})
	calls := 0
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, &cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
		}
		return []byte("ok"), nil
	}
	_, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	if !errors.Is(err, ErrISCSIAdmStillRunning) || !cmdexec.IsStillRunning(err) {
		t.Fatalf("first call error = %v, want ErrISCSIAdmStillRunning", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := service.runISCSIAdm(ctx, time.Second, "-m", "node", "-P", "0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second call while the first still runs = %v, want to time out waiting for the gate", err)
	}

	close(exited)
	if out, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "node", "-P", "0"); err != nil || string(out) != "ok" {
		t.Fatalf("call after the first exited = %q, %v", out, err)
	}
}

func TestRunISCSIAdmRetriesOnceAfterClearingStaleLock(t *testing.T) {
	service := &NodeService{}
	var calls, clears int
	service.clearStaleISCSIDBLockFn = func() bool {
		clears++
		return clears >= 2 // the pre-check finds nothing; the post-failure check clears it
	}
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), errors.New("exit status 6")
		}
		return []byte("records"), nil
	}
	out, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "node", "-P", "0")
	if err != nil || string(out) != "records" || calls != 2 {
		t.Fatalf("runISCSIAdm() = %q, %v after %d calls; want a single retry after the lock was cleared", out, err, calls)
	}
}

func TestRunISCSIAdmReportsHeldDatabaseLock(t *testing.T) {
	service := &NodeService{}
	calls := 0
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), errors.New("exit status 6")
	}
	_, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "node", "-P", "0")
	if !errors.Is(err, ErrISCSINodeDBLocked) {
		t.Fatalf("runISCSIAdm() error = %v, want ErrISCSINodeDBLocked", err)
	}
	if calls != 1 {
		t.Fatalf("iscsiadm calls = %d; nothing was cleared, so there must be no retry", calls)
	}
	if strings.Contains(err.Error(), "TrueNAS") {
		t.Fatalf("error %q blames TrueNAS for a local lock", err)
	}
}

func TestRunISCSIAdmGateWaitHonoursContext(t *testing.T) {
	service := &NodeService{}
	release := make(chan struct{})
	entered := make(chan struct{})
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		close(entered)
		<-release
		return nil, nil
	}
	go func() { _, _ = service.runISCSIAdm(context.Background(), time.Second, "-m", "node", "-P", "0") }()
	<-entered
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.runISCSIAdm(ctx, time.Second, "-m", "node", "-P", "0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runISCSIAdm() with canceled ctx = %v, want context.Canceled", err)
	}
}

// Once the RPC is canceled, staging must stop retrying instead of sleeping through its
// remaining attempts and piling more iscsiadm work onto the node.
func TestStageISCSIVolumeStopsRetryingWhenContextEnds(t *testing.T) {
	service := &NodeService{}
	logins := 0
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "--login"):
			logins++
			return []byte("iscsiadm: initiator reported error (8 - connection timed out)"), errors.New("exit status 8")
		case slices.Contains(args, "node"):
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	request := &csi.NodeStageVolumeRequest{
		VolumeId: "tank/csi/volume", StagingTargetPath: filepath.Join(t.TempDir(), "globalmount"),
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
		VolumeContext:    map[string]string{VolumeContextKeyProtocol: ProtocolISCSI, VolumeContextKeyISCSIIQN: testISCSIIQN, "server": "192.0.2.10", "port": "3260"},
	}
	start := time.Now()
	_, err := service.NodeStageVolume(ctx, request)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("NodeStageVolume() returned after %v; retries outlived the RPC", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "connection timed out") {
		t.Fatalf("NodeStageVolume() = %v; want the last real login error, not only the context error", err)
	}
	if logins == 0 {
		t.Fatal("login never attempted")
	}
}

// A command left running can already have printed open-iscsi's lock timeout. Starting a
// second iscsiadm then would run two at once and lose track of the first (review finding).
func TestRunISCSIAdmDoesNotRetryWhileFirstAttemptStillRuns(t *testing.T) {
	service := &NodeService{clearStaleISCSIDBLockFn: func() bool { return true }}
	exited := make(chan struct{})
	calls := 0
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"),
			&cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
	}
	_, err := service.runISCSIAdm(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	if !errors.Is(err, ErrISCSIAdmStillRunning) {
		t.Fatalf("runISCSIAdm() = %v, want ErrISCSIAdmStillRunning", err)
	}
	if calls != 1 {
		t.Fatalf("iscsiadm started %d times while the first was still running", calls)
	}
	close(exited)
}

func TestCheckISCSIAdmDoesNotCallSlownessMissing(t *testing.T) {
	service := &NodeService{}
	exited := make(chan struct{})
	defer close(exited)
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return nil, &cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
	}
	if err := service.checkISCSIAdm(context.Background()); errors.Is(err, ErrISCSIAdmNotFound) {
		t.Fatalf("checkISCSIAdm() = %v; a slow iscsiadm is not a missing one", err)
	}
}

// Session-query failures are not "no device": they must not be hidden as ErrISCSIDeviceNotFound.
func TestFindISCSIDeviceReportsQueryFailures(t *testing.T) {
	service := &NodeService{}
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	noSessions := exitError(t, iscsiErrNoObjsFound)
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("iscsiadm: No active sessions."), noSessions
	}
	if _, err := service.findISCSIDevice(context.Background(), params); !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("no sessions = %v, want ErrISCSIDeviceNotFound", err)
	}
	failed := exitError(t, 4)
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("iscsiadm: could not connect to iscsid"), failed
	}
	_, err := service.findISCSIDevice(context.Background(), params)
	if errors.Is(err, ErrISCSIDeviceNotFound) || !errors.Is(err, errISCSISessionQuery) {
		t.Fatalf("query failure = %v, want errISCSISessionQuery", err)
	}
}

func TestLeftRunningWithoutVolumeIsInternal(t *testing.T) {
	s := &NodeService{}
	exited := make(chan struct{})
	defer close(exited)
	err := s.leftRunning(context.Background(), "format of /dev/sdb",
		&cmdexec.StillRunningError{Name: "mkfs", Done: exited, Err: context.DeadlineExceeded})
	if status.Code(err) != codes.Internal {
		t.Fatalf("leftRunning() without a volume = %v, want Internal (it cannot be tracked)", err)
	}
}
