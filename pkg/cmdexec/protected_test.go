//go:build linux || darwin

package cmdexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunProtectedReturnsOutputOfFastCommand(t *testing.T) {
	out, err := RunProtected(context.Background(), time.Second, 0, "sh", "-c", "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("RunProtected() error = %v", err)
	}
	if got := string(out); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("RunProtected() = %q, want stdout and stderr combined", got)
	}
}

func TestRunProtectedKeepsExitCode(t *testing.T) {
	// With a hard ceiling the process context is canceled as soon as the command exits; that
	// must not turn a plain exit 21 (iscsiadm "No records found") into a reported kill.
	for _, hardCeiling := range []time.Duration{0, time.Minute} {
		for range 20 {
			_, err := RunProtected(context.Background(), time.Second, hardCeiling, "sh", "-c", "exit 21")
			if code, ok := ExitCode(err); !ok || code != 21 {
				t.Fatalf("hardCeiling %v: ExitCode(%v) = %d, %v; want 21, true", hardCeiling, err, code, ok)
			}
			if IsStillRunning(err) {
				t.Error("IsStillRunning() = true for a command that exited")
			}
		}
	}
}

// The soft deadline must never signal the process: killing a command that holds a
// lock file or is half way through writing a superblock leaves damage behind (#336).
func TestRunProtectedLeavesProcessRunningAfterSoftDeadline(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "finished")
	start := time.Now()
	_, err := RunProtected(context.Background(), 50*time.Millisecond, 0, "sh", "-c", "sleep 0.4; touch "+marker)
	if !IsStillRunning(err) {
		t.Fatalf("RunProtected() error = %v, want still running", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("RunProtected() returned after %v, want shortly after the soft deadline", elapsed)
	}
	if !errors.Is(err, ErrStillRunning) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v should wrap ErrStillRunning and context.DeadlineExceeded", err)
	}
	if IsTimeout(err) {
		// IsTimeout means "killed for its deadline", which did not happen.
		t.Error("IsTimeout() = true for a command that was left running")
	}
	done, ok := StillRunningDone(err)
	if !ok {
		t.Fatal("StillRunningDone() = false")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done never closed")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("process did not run to completion: %v", statErr)
	}
}

func TestRunProtectedCallerCancellationDoesNotKill(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "finished")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := RunProtected(ctx, time.Minute, 0, "sh", "-c", "sleep 0.3; touch "+marker)
	if !IsStillRunning(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("RunProtected() error = %v, want still running wrapping context.Canceled", err)
	}
	done, _ := StillRunningDone(err)
	<-done
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("canceled caller killed the process: %v", statErr)
	}
}

func TestRunProtectedHardCeilingKills(t *testing.T) {
	_, err := RunProtected(context.Background(), 0, 100*time.Millisecond, "sleep", "10")
	if err == nil || IsStillRunning(err) {
		t.Fatalf("RunProtected() error = %v, want a hard-ceiling kill", err)
	}
	if !IsTimeout(err) {
		t.Errorf("IsTimeout(%v) = false for a hard-ceiling kill", err)
	}
}

// Output of a command that finished is parsed by callers (iscsiadm -m session -P 3 on a node
// with many sessions is well over 64 KiB), so it must come back complete (review finding).
func TestRunProtectedReturnsFullOutputOfFinishedCommand(t *testing.T) {
	const size = 4 * ProtectedOutputLimit
	out, err := RunProtected(context.Background(), 5*time.Second, 0, "sh", "-c", "head -c "+strconv.Itoa(size)+" /dev/zero")
	if err != nil {
		t.Fatalf("RunProtected() error = %v", err)
	}
	if len(out) != size {
		t.Fatalf("output length %d, want the full %d bytes", len(out), size)
	}
}

// Only a command left running has its further output capped, since nobody waits to read it.
func TestRunProtectedCapsOutputAfterCallerLeaves(t *testing.T) {
	_, err := RunProtected(context.Background(), 50*time.Millisecond, 0, "sh", "-c",
		"sleep 0.2; head -c "+strconv.Itoa(4*ProtectedOutputLimit)+" /dev/zero")
	var still *StillRunningError
	if !errors.As(err, &still) {
		t.Fatalf("RunProtected() = %v, want *StillRunningError", err)
	}
	<-still.Done
	out, _ := still.Result()
	if len(out) > ProtectedOutputLimit {
		t.Fatalf("output after the caller left grew to %d bytes, want at most %d", len(out), ProtectedOutputLimit)
	}
}

func TestStillRunningErrorHelpersOnOtherErrors(t *testing.T) {
	if IsStillRunning(errors.New("x")) {
		t.Error("IsStillRunning() = true for a plain error")
	}
	if _, ok := StillRunningDone(nil); ok {
		t.Error("StillRunningDone(nil) = true")
	}
}

// A command left running must still report how it ended, so its failure is not silent.
func TestStillRunningErrorReportsResultAfterDone(t *testing.T) {
	_, err := RunProtected(context.Background(), 50*time.Millisecond, 0, "sh", "-c", "sleep 0.2; echo late; exit 3")
	var still *StillRunningError
	if !errors.As(err, &still) {
		t.Fatalf("RunProtected() = %v, want *StillRunningError", err)
	}
	<-still.Done
	out, resultErr := still.Result()
	if code, ok := ExitCode(resultErr); !ok || code != 3 {
		t.Fatalf("Result() error = %v, want exit 3", resultErr)
	}
	if !strings.Contains(string(out), "late") {
		t.Fatalf("Result() output = %q, want the output written after the caller left", out)
	}
}
