package driver

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/fenio/tns-csi/pkg/cmdexec"
	"k8s.io/klog/v2"
)

// mountBinary and mountWaitDelay are variables so tests can substitute a fake mount.
var (
	mountBinary    = "mount"
	mountWaitDelay = cmdexec.DefaultWaitDelay
)

// blockStageMountTimeout bounds staging an iSCSI or NVMe-oF filesystem. An ext4 MMP takeover
// after an unclean holder waits ~2*(2*mmp_check_interval+1) s (~42 s seen in #299); killing it
// restarts the wait. It stays below the kubelet's 2 min NodeStage deadline.
const blockStageMountTimeout = 100 * time.Second

// runMount runs mount(8) with args (the last one is the target), bounded by timeout, and
// returns its combined output.
//
// mount helpers can start daemons that inherit mount's output pipe (mount.nfs starting
// rpc.statd, iscsiadm starting iscsid). mount itself then exits 0 but the pipe stays open,
// and once WaitDelay expires the command reports exec.ErrWaitDelay. In that case the mount
// table decides: if the target is mounted, the mount succeeded.
func runMount(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	mountCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := cmdexec.CommandContext(mountCtx, mountBinary, args...)
	cmd.WaitDelay = mountWaitDelay
	output, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) && len(args) > 0 {
		target := args[len(args)-1]
		if _, mounted, lookupErr := lookupMountedEntry(ctx, target); lookupErr == nil && mounted {
			klog.Warningf("mount %s exited but a helper kept its output open; target is mounted, treating as success", target)
			return output, nil
		}
	}
	return output, err
}
