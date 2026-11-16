package cmdexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ProtectedOutputLimit caps the output a command keeps adding after RunProtected has stopped
// waiting for it, so a command left running cannot grow without bound. The output of a
// command that finishes while its caller waits is returned in full: callers parse it.
const ProtectedOutputLimit = 64 * 1024

// ErrStillRunning reports that a protected command was still running when its caller's
// deadline passed. The process was not signaled.
var ErrStillRunning = errors.New("command still running")

// StillRunningError is returned by RunProtected when ctx ends before the command exits.
// Done is closed once the process has exited. Callers that guard a resource with the
// command (a lock, a device) must keep holding it until Done closes.
type StillRunningError struct {
	Done   <-chan struct{}
	Err    error // the context error that ended the wait
	result func() ([]byte, error)
	Name   string
}

// Result returns the command's capped output and exit error once Done is closed, so a
// command left running does not fail silently. Before Done closes it returns ErrStillRunning.
func (e *StillRunningError) Result() ([]byte, error) {
	select {
	case <-e.Done:
	default:
		return nil, ErrStillRunning
	}
	if e.result == nil {
		return nil, nil
	}
	return e.result()
}

func (e *StillRunningError) Error() string {
	return fmt.Sprintf("%s: %v; left running", e.Name, e.Err)
}

// Is makes errors.Is(err, ErrStillRunning) hold.
func (e *StillRunningError) Is(target error) bool { return target == ErrStillRunning }

func (e *StillRunningError) Unwrap() error { return e.Err }

// IsStillRunning reports whether err came from a protected command left running.
func IsStillRunning(err error) bool {
	return errors.Is(err, ErrStillRunning)
}

// StillRunningDone returns the channel that closes when the command behind err exits.
func StillRunningDone(err error) (<-chan struct{}, bool) {
	var still *StillRunningError
	if errors.As(err, &still) {
		return still.Done, true
	}
	return nil, false
}

// RunProtected runs name with args for commands that must not be killed part way: killing
// them leaves damage that does not heal by itself. iscsiadm holding open-iscsi's link()
// based DB lock (#336), mkfs writing a superblock and resize2fs are such commands.
//
// softTimeout (in addition to ctx) bounds how long the caller waits, not how long the
// command runs: when it passes, RunProtected returns a *StillRunningError and the process
// keeps going. hardCeiling > 0 is the only kill, a backstop for a command that is truly
// hung; 0 means the command is never killed. Combined output is returned in full; only
// what a command left running writes afterwards is capped (ProtectedOutputLimit).
func RunProtected(ctx context.Context, softTimeout, hardCeiling time.Duration, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	waitCtx := ctx
	if softTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, softTimeout)
		defer cancel()
	}

	procCtx := context.WithoutCancel(ctx)
	procCancel := context.CancelFunc(func() {})
	if hardCeiling > 0 {
		procCtx, procCancel = context.WithTimeout(procCtx, hardCeiling)
	}

	output := &cappedBuffer{}
	cmd := CommandContext(procCtx, name, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		procCancel()
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	done := make(chan struct{})
	var waitErr error
	go func() {
		defer procCancel()
		waitErr = cmd.Wait()
		close(done)
	}()

	// result is valid once done is closed: close(done) orders the writes to waitErr before it.
	result := func() ([]byte, error) {
		if waitErr != nil {
			if ctxErr := procCtx.Err(); ctxErr != nil {
				return output.Bytes(), fmt.Errorf("%s: killed at hard ceiling %v: %w (%w)", name, hardCeiling, ctxErr, waitErr)
			}
			return output.Bytes(), fmt.Errorf("%s: %w", name, waitErr)
		}
		return output.Bytes(), nil
	}
	select {
	case <-done:
		return result()
	case <-waitCtx.Done():
		// select picks at random when both are ready: a command that exited just as the
		// deadline fired has finished, and its result must not be reported as still running.
		select {
		case <-done:
			return result()
		default:
		}
		output.capFurther(ProtectedOutputLimit)
		return output.Bytes(), &StillRunningError{Name: name, Done: done, Err: waitCtx.Err(), result: result}
	}
}

// cappedBuffer collects combined output. It keeps everything until capFurther is called,
// then accepts at most that many more bytes. It is safe for concurrent writes and reads,
// since a protected command may still be writing when the caller reads what it has so far.
type cappedBuffer struct {
	buf   bytes.Buffer
	capAt int // 0: unlimited
	mu    sync.Mutex
}

// capFurther limits what the buffer accepts from now on to limit more bytes.
func (c *cappedBuffer) capFurther(limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capAt = c.buf.Len() + limit
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capAt == 0 {
		_, _ = c.buf.Write(p)
		return len(p), nil
	}
	if remaining := c.capAt - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = c.buf.Write(p[:remaining])
		} else {
			_, _ = c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}
