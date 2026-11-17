package driver

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fenio/tns-csi/pkg/cmdexec"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Commands such as mkfs, resize2fs and e2fsck are not killed when an RPC's deadline passes
// (cmdexec.RunProtected): killing them part way leaves damage behind. A command left running
// keeps working on its volume after the RPC returns, so later stage, unstage and expand calls
// for that volume answer Aborted (CSI's "operation pending", which kubelet retries) until it
// exits. Without that, an unstage could log out the device a format is still writing to.

// leftRunningOps tracks commands left running per volume ID. Volume IDs, not device paths,
// are the key: an iSCSI device can come back under a different /dev name after a re-login.
type leftRunningOps struct {
	ops map[string]leftRunningOp
	mu  sync.Mutex
}

type leftRunningOp struct {
	since time.Time
	done  <-chan struct{}
	what  string
}

type volumeOpKey struct{}

// withVolumeOp records which volume an RPC operates on, so helpers that only see a device
// can register a command left running against the volume.
func withVolumeOp(ctx context.Context, volumeID string) context.Context {
	return context.WithValue(ctx, volumeOpKey{}, volumeID)
}

// checkNoLeftRunningOp returns Aborted while a command left running for volumeID is alive.
func (s *NodeService) checkNoLeftRunningOp(volumeID string) error {
	r := &s.leftRunningOps
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[volumeID]
	if !ok {
		return nil
	}
	select {
	case <-op.done:
		delete(r.ops, volumeID)
		return nil
	default:
		return status.Errorf(codes.Aborted, "%s for volume %s is still running from an earlier request (for %v); retry once it finishes",
			op.what, volumeID, time.Since(op.since).Round(time.Second))
	}
}

// leftRunning handles err from a protected command. If the command was left running, it is
// registered against the request's volume and an Aborted status is returned; otherwise nil.
func (s *NodeService) leftRunning(ctx context.Context, what string, err error) error {
	done, running := cmdexec.StillRunningDone(err)
	if !running {
		return nil
	}
	volumeID, ok := ctx.Value(volumeOpKey{}).(string)
	if !ok || volumeID == "" {
		// Without a volume the command cannot be tracked, and a retry could start a second
		// one on the same device. Every RPC that runs protected commands sets the volume.
		klog.Errorf("BUG: %s was left running outside a volume operation; it cannot be tracked: %v", what, err)
		go logLeftRunningResult(err, what)
		return status.Errorf(codes.Internal, "%s is still running and cannot be tracked: %v", what, err)
	}
	klog.Warningf("%s for volume %s did not finish before the request deadline; leaving it to complete rather than killing it part way: %v", what, volumeID, err)
	r := &s.leftRunningOps
	r.mu.Lock()
	if r.ops == nil {
		r.ops = make(map[string]leftRunningOp)
	}
	r.ops[volumeID] = leftRunningOp{done: done, what: what, since: time.Now()}
	r.mu.Unlock()
	go func() {
		logLeftRunningResult(err, what+" for volume "+volumeID)
		r.mu.Lock()
		if current, exists := r.ops[volumeID]; exists && current.done == done {
			delete(r.ops, volumeID)
		}
		r.mu.Unlock()
	}()
	return status.Errorf(codes.Aborted, "%s is still running; retry once it finishes: %v", what, err)
}

// logLeftRunningResult waits for a command left running to exit and logs how it ended, so a
// failure in the background (including a kill at its hard ceiling) is not silent.
func logLeftRunningResult(err error, what string) {
	var still *cmdexec.StillRunningError
	if !errors.As(err, &still) {
		return
	}
	<-still.Done
	output, resultErr := still.Result()
	if resultErr != nil {
		klog.Warningf("%s, left running after its request ended, failed: %v, output: %s", what, resultErr, bytes.TrimSpace(output))
		return
	}
	klog.Infof("%s, left running after its request ended, completed successfully", what)
}
