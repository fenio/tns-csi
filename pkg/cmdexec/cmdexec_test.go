//go:build linux || darwin

package cmdexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCombinedOutputSuccess(t *testing.T) {
	out, err := CombinedOutput(context.Background(), time.Second, "sh", "-c", "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("CombinedOutput() error = %v", err)
	}
	if got := string(out); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("CombinedOutput() = %q, want stdout and stderr combined", got)
	}
}

func TestCombinedOutputNonZeroExitIsNotATimeout(t *testing.T) {
	out, err := CombinedOutput(context.Background(), time.Second, "sh", "-c", "echo boom; exit 3")
	if err == nil {
		t.Fatal("CombinedOutput() error = nil, want exit error")
	}
	if code, ok := ExitCode(err); !ok || code != 3 {
		t.Errorf("ExitCode() = %d, %v; want 3, true", code, ok)
	}
	if IsTimeout(err) {
		t.Error("IsTimeout() = true for a plain non-zero exit")
	}
	if !strings.Contains(string(out), "boom") {
		t.Errorf("output %q lost on non-zero exit", out)
	}
}

// A command killed for exceeding its timeout must be reported as a timeout, never as an
// ordinary exit: callers have mistaken a killed probe for a definitive answer before.
func TestCombinedOutputTimeoutIsDistinguishable(t *testing.T) {
	_, err := CombinedOutput(context.Background(), 100*time.Millisecond, "sleep", "10")
	if !IsTimeout(err) {
		t.Fatalf("IsTimeout(%v) = false, want true", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout error %v does not wrap context.DeadlineExceeded", err)
	}
	if _, ok := ExitCode(err); ok {
		t.Error("ExitCode() reported an exit code for a timed-out command")
	}
}

func TestCombinedOutputHonoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CombinedOutput(ctx, time.Minute, "sleep", "10")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CombinedOutput() on a cancelled context = %v, want context.Canceled", err)
	}
}

// The classic hang: the command's own process is killed at the deadline, but a child
// it spawned keeps the output pipe open, so a plain exec.CommandContext + CombinedOutput
// blocks until that child exits. With WaitDelay the call returns shortly after the
// deadline regardless.
func TestCombinedOutputDoesNotHangOnInheritedPipes(t *testing.T) {
	start := time.Now()
	_, err := CombinedOutput(context.Background(), 200*time.Millisecond, "sh", "-c", "sleep 30 & sleep 30")
	elapsed := time.Since(start)

	if !IsTimeout(err) {
		t.Errorf("IsTimeout(%v) = false, want true", err)
	}
	if limit := 200*time.Millisecond + DefaultWaitDelay + 3*time.Second; elapsed > limit {
		t.Fatalf("CombinedOutput() returned after %v; want < %v (pipe held open by a grandchild)", elapsed, limit)
	}
}

func TestCombinedOutputMissingBinary(t *testing.T) {
	_, err := CombinedOutput(context.Background(), time.Second, "definitely-not-a-real-binary-tns-csi")
	if err == nil {
		t.Fatal("CombinedOutput() error = nil for a missing binary")
	}
	if IsTimeout(err) {
		t.Error("missing binary reported as timeout")
	}
	if _, ok := ExitCode(err); ok {
		t.Error("missing binary reported an exit code")
	}
}
