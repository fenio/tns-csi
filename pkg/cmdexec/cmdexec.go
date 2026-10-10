// Package cmdexec runs external commands with bounded lifetimes and unambiguous errors.
package cmdexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultWaitDelay bounds how long CombinedOutput waits, after the command is killed,
// for its output pipes to close. Without it, a child process that inherited the pipes
// (e.g. a helper forked by mount or nvme) keeps the call blocked long after the deadline.
const DefaultWaitDelay = 5 * time.Second

// CombinedOutput runs name with args and returns its combined stdout and stderr.
// timeout bounds the command in addition to ctx; 0 means ctx alone.
//
// The call returns at most DefaultWaitDelay after the deadline even if the process or
// its children do not exit. When the command is stopped by its deadline or by ctx
// cancellation, the error wraps the context error so IsTimeout / errors.Is can tell it
// apart from an ordinary non-zero exit (which a killed process otherwise looks like).
func CombinedOutput(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = DefaultWaitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, fmt.Errorf("%s: %w (%w)", name, ctxErr, err)
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// IsTimeout reports whether err came from a command killed for exceeding its deadline.
func IsTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// ExitCode returns the exit code of a command that ran to completion and exited
// non-zero. It reports false for commands that were killed (timeout, cancellation,
// signal) or never started, so callers cannot mistake a killed probe for an answer.
func ExitCode(err error) (int, bool) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
