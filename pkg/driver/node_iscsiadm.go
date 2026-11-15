package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fenio/tns-csi/pkg/cmdexec"
	"github.com/fenio/tns-csi/pkg/metrics"
	"k8s.io/klog/v2"
)

// All iscsiadm calls go through runISCSIAdm (enforced by forbidigo). Two rules come from
// open-iscsi's node-database lock, a link()-based lock file that survives a killed holder
// (see node_iscsi_dblock.go, #336):
//
//  1. Never kill iscsiadm on a deadline. A per-call timeout bounds how long the RPC waits;
//     the process keeps running (cmdexec.RunProtected) and only a hung one is killed, at
//     iscsiadmHardCeiling.
//  2. Run database operations one at a time per node. The database is single-writer anyway,
//     so parallel calls only queue on the lock file and burn their timeouts doing so.
const (
	// iscsiadmHardCeiling kills an iscsiadm that is truly hung. It is far above open-iscsi's
	// 30 s lock wait plus a login's own retries.
	iscsiadmHardCeiling = 3 * time.Minute
	// iscsiadmDBTimeout is the wait budget for a database operation. It exceeds open-iscsi's
	// 30 s lock wait, so a call queued behind a foreign lock holder reports open-iscsi's
	// error rather than our deadline.
	iscsiadmDBTimeout = 45 * time.Second
	// iscsiadmQueryTimeout is the wait budget for session queries, which read sysfs.
	iscsiadmQueryTimeout = 10 * time.Second
	// iscsiadmQueryHardCeiling bounds session queries and logouts so a hung sysfs cannot pile
	// up processes from polling loops. It is still safe for the lock: an iscsiadm waiting on
	// it gives up by itself after 30 s, and none holds it for anything close to a minute.
	iscsiadmQueryHardCeiling = time.Minute

	iscsiDBLockTimeoutMessage = "Timeout on acquiring lock on DB"
)

// iscsiadmDBModes are the iscsiadm modes that read or write the node database under its lock.
var iscsiadmDBModes = map[string]bool{"discovery": true, "discoverydb": true, "node": true, "iface": true}

// iscsiadmNeedsDBLock reports whether args run an iscsiadm mode that uses the node database.
func iscsiadmNeedsDBLock(args []string) bool {
	for i, arg := range args {
		switch {
		case (arg == "-m" || arg == "--mode") && i+1 < len(args):
			return iscsiadmDBModes[args[i+1]]
		case strings.HasPrefix(arg, "--mode="):
			return iscsiadmDBModes[strings.TrimPrefix(arg, "--mode=")]
		}
	}
	return false
}

// iscsiadmCommand returns the command line that runs iscsiadm in the host's namespaces when
// the plugin runs in a container with hostPID, so the host's iscsid and /etc/iscsi are used.
// nsenter does not fork, so the process the driver waits on is iscsiadm itself.
func iscsiadmCommand(args []string) (name string, cmdArgs []string) {
	if _, err := os.Stat("/proc/1/ns/mnt"); err == nil {
		nsenterArgs := make([]string, 0, 4+len(args))
		nsenterArgs = append(nsenterArgs, "--mount=/proc/1/ns/mnt", "--ipc=/proc/1/ns/ipc", "--", "iscsiadm")
		nsenterArgs = append(nsenterArgs, args...)
		klog.V(5).Infof("Running iscsiadm via nsenter: nsenter %v", nsenterArgs)
		return "nsenter", nsenterArgs
	}
	klog.V(5).Infof("Running iscsiadm directly: iscsiadm %v", args)
	return "iscsiadm", args
}

// iscsiadmGate serializes node-database iscsiadm calls. The zero value is ready to use.
type iscsiadmGate struct {
	ch   chan struct{}
	once sync.Once
}

func (g *iscsiadmGate) acquire(ctx context.Context) error {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
	select {
	case g.ch <- struct{}{}:
		return nil
	default:
	}
	metrics.ISCSIAdmWaiting()
	defer metrics.ISCSIAdmDoneWaiting()
	select {
	case g.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *iscsiadmGate) release() { <-g.ch }

// runISCSIAdm runs iscsiadm with args. timeout bounds how long the caller waits; for database
// operations it starts once this call owns the node's iscsiadm gate. A command still running
// at the deadline is left to finish and reported as ErrISCSIAdmStillRunning; for database
// operations the gate stays held until it exits.
func (s *NodeService) runISCSIAdm(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if !iscsiadmNeedsDBLock(args) {
		output, err := s.execISCSIAdm(ctx, timeout, iscsiadmQueryHardCeiling, args)
		if cmdexec.IsStillRunning(err) {
			go logLeftRunningResult(err, "iscsiadm "+strings.Join(args, " "))
		}
		return output, wrapISCSIAdmStillRunning(err, timeout)
	}

	if err := s.iscsiadmGate.acquire(ctx); err != nil {
		return nil, fmt.Errorf("waiting for this node's iscsiadm gate: %w", err)
	}
	holding := true
	defer func() {
		if holding {
			s.iscsiadmGate.release()
		}
	}()

	s.clearStaleISCSIDBLock()
	output, err := s.execISCSIAdm(ctx, timeout, iscsiadmHardCeiling, args)
	// A command still running may already have printed the lock timeout; retrying then would
	// start a second iscsiadm alongside it.
	if err != nil && !cmdexec.IsStillRunning(err) && isISCSIDBLockTimeout(output) {
		// Retry inside this hold: calling runISCSIAdm again would wait on our own gate.
		if s.clearStaleISCSIDBLock() {
			output, err = s.execISCSIAdm(ctx, timeout, iscsiadmHardCeiling, args)
		}
		if err != nil && isISCSIDBLockTimeout(output) {
			err = fmt.Errorf("%w: %w, output: %s", ErrISCSINodeDBLocked, err, strings.TrimSpace(string(output)))
		}
	}

	if done, running := cmdexec.StillRunningDone(err); running {
		holding = false
		go func() {
			<-done
			s.iscsiadmGate.release()
			logLeftRunningResult(err, "iscsiadm "+strings.Join(args, " "))
		}()
	}
	return output, wrapISCSIAdmStillRunning(err, timeout)
}

func (s *NodeService) execISCSIAdm(ctx context.Context, timeout, hardCeiling time.Duration, args []string) ([]byte, error) {
	if s.runISCSIAdmFn != nil {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return s.runISCSIAdmFn(callCtx, args...)
	}
	name, cmdArgs := iscsiadmCommand(args)
	return cmdexec.RunProtected(ctx, timeout, hardCeiling, name, cmdArgs...)
}

func (s *NodeService) clearStaleISCSIDBLock() bool {
	if s.clearStaleISCSIDBLockFn != nil {
		return s.clearStaleISCSIDBLockFn()
	}
	return false
}

func isISCSIDBLockTimeout(output []byte) bool {
	return strings.Contains(string(output), iscsiDBLockTimeoutMessage)
}

func wrapISCSIAdmStillRunning(err error, timeout time.Duration) error {
	if err == nil || !cmdexec.IsStillRunning(err) {
		return err
	}
	return fmt.Errorf("%w after %v; left running so open-iscsi's node database lock is not orphaned: %w",
		ErrISCSIAdmStillRunning, timeout, err)
}

// iscsiadmErrorIs reports whether err is one of the iscsiadm conditions that are not about
// the target at all (lock held, command left running), for callers choosing what to report.
func iscsiadmErrorIs(err error) bool {
	return errors.Is(err, ErrISCSINodeDBLocked) || errors.Is(err, ErrISCSIAdmStillRunning)
}
