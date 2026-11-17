//go:build linux

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// dbLockFixture builds a fake host root (with run/lock/iscsi) and a fake /proc.
type dbLockFixture struct {
	ctime   time.Time
	root    string
	proc    string
	lockDir string
}

func newDBLockFixture(t *testing.T, withWriteLock bool) *dbLockFixture {
	t.Helper()
	f := &dbLockFixture{root: t.TempDir(), proc: t.TempDir()}
	f.lockDir = filepath.Join(f.root, "run", "lock", "iscsi")
	if err := os.MkdirAll(f.lockDir, 0o750); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(f.lockDir, "lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if withWriteLock {
		if err := os.Link(lock, filepath.Join(f.lockDir, "lock.write")); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(lock)
	if err != nil {
		t.Fatal(err)
	}
	f.ctime, _ = fileChangeTime(info)
	// Boot time 1000 s before the lock was linked.
	f.writeProc(t, "stat", fmt.Sprintf("cpu  1 2 3\nbtime %d\n", f.ctime.Unix()-1000))
	return f
}

func (f *dbLockFixture) writeProc(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(f.proc, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// addProcess adds a process that started offset seconds after boot.
func (f *dbLockFixture) addProcess(t *testing.T, pid int, comm string, startAfterBoot time.Duration) {
	t.Helper()
	f.addProcessInState(t, pid, comm, "S", startAfterBoot)
}

// addProcessInState adds a process in the given /proc state (S sleeping, D uninterruptible, R running).
func (f *dbLockFixture) addProcessInState(t *testing.T, pid int, comm, state string, startAfterBoot time.Duration) {
	t.Helper()
	ticks := int64(startAfterBoot/time.Second) * procClockTicks
	dir := strconv.Itoa(pid)
	f.writeProc(t, filepath.Join(dir, "comm"), comm+"\n")
	// Field 2 contains spaces and a ')' on purpose: parsing must anchor on the last ')'.
	stat := fmt.Sprintf("%d (%s) x) %s 1 1 1 0 -1 4194560 100 0 0 0 0 0 0 0 20 0 1 0 %d 0 0", pid, comm, state, ticks)
	f.writeProc(t, filepath.Join(dir, "stat"), stat)
}

func (f *dbLockFixture) janitor(nowAfterLock time.Duration) *iscsiDBLockJanitor {
	return &iscsiDBLockJanitor{
		roots:      []string{f.root},
		procDir:    f.proc,
		staleAfter: 60 * time.Second,
		now:        func() time.Time { return f.ctime.Add(nowAfterLock) },
	}
}

func (f *dbLockFixture) writeLockExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.lockDir, "lock.write"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestISCSIDBLockJanitorRemovesOrphanedLock(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.addProcess(t, 10, "iscsid", 5*time.Second) // long-lived daemon, not iscsiadm
	if !f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() = false, want an orphaned lock removed")
	}
	if f.writeLockExists(t) {
		t.Fatal("lock.write still present")
	}
	if _, err := os.Lstat(filepath.Join(f.lockDir, "lock")); err != nil {
		t.Fatalf("janitor must never remove the base lock file: %v", err)
	}
}

func TestISCSIDBLockJanitorKeepsFreshLock(t *testing.T) {
	f := newDBLockFixture(t, true)
	if f.janitor(10 * time.Second).clearStale() {
		t.Fatal("clearStale() removed a lock younger than the stale threshold")
	}
	if !f.writeLockExists(t) {
		t.Fatal("lock.write removed")
	}
}

func TestISCSIDBLockJanitorKeepsLockWithOlderLiveIscsiadm(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.addProcess(t, 42, "iscsiadm", 900*time.Second) // started 100 s before the lock was linked
	if f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() removed a lock a live iscsiadm may own")
	}
}

func TestISCSIDBLockJanitorIgnoresWaitersStartedAfterLock(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.addProcess(t, 43, "iscsiadm", 1300*time.Second) // started 300 s after the lock: only a waiter
	if !f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() = false; an iscsiadm started after the lock cannot own it")
	}
}

func TestISCSIDBLockJanitorTreatsUnknownStartTimeAsOwner(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.writeProc(t, "44/comm", "iscsiadm\n")
	f.writeProc(t, "44/stat", "garbage")
	if f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() removed a lock while an iscsiadm of unknown age is alive")
	}
}

func TestISCSIDBLockJanitorNeverRemovesWithoutProcVisibility(t *testing.T) {
	f := newDBLockFixture(t, true)
	j := f.janitor(10 * time.Minute)
	j.procDir = filepath.Join(f.proc, "missing")
	if j.clearStale() {
		t.Fatal("clearStale() removed a lock without being able to scan processes")
	}
}

func TestISCSIDBLockJanitorKeepsLockReplacedBeforeUnlink(t *testing.T) {
	f := newDBLockFixture(t, true)
	j := f.janitor(10 * time.Minute)
	j.beforeUnlink = func() {
		// A real owner released and re-took the lock between the check and the unlink.
		later := time.Now().Add(time.Hour)
		writeLock := filepath.Join(f.lockDir, "lock.write")
		if err := os.Chmod(writeLock, 0o640); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(writeLock, later, later)
	}
	// ctime only moves forward, and Chmod bumps it to "now"; make sure that is after the
	// recorded ctime even on coarse clocks.
	time.Sleep(20 * time.Millisecond)
	if j.clearStale() {
		t.Fatal("clearStale() removed a lock whose ctime changed after it was judged stale")
	}
	if !f.writeLockExists(t) {
		t.Fatal("lock.write removed")
	}
}

func TestISCSIDBLockJanitorNoLock(t *testing.T) {
	f := newDBLockFixture(t, false)
	if f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() = true with no lock.write")
	}
}

func TestISCSIDBLockJanitorDoesNotFollowAbsoluteSymlinkOutOfRoot(t *testing.T) {
	f := newDBLockFixture(t, true)
	outside := newDBLockFixture(t, true)
	// Replace the root's run/lock/iscsi with an absolute symlink to a directory outside it.
	if err := os.RemoveAll(f.lockDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside.lockDir, f.lockDir); err != nil {
		t.Fatal(err)
	}
	j := f.janitor(10 * time.Minute)
	j.now = func() time.Time { return outside.ctime.Add(10 * time.Minute) }
	j.clearStale()
	if !outside.writeLockExists(t) {
		t.Fatal("janitor followed a symlink out of the host root and removed a foreign lock")
	}
}

// Debian, Ubuntu, Fedora and Arch ship /var/lock as an absolute symlink to /run/lock. Under
// /proc/1/root that symlink means <host root>/run/lock, which is how the host itself sees it;
// refusing it (as os.Root does) misreports a normal host as uninspectable on every call.
func TestResolveInRootReadsAbsoluteSymlinksAsHostPaths(t *testing.T) {
	f := newDBLockFixture(t, false)
	if err := os.MkdirAll(filepath.Join(f.root, "var"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/run/lock", filepath.Join(f.root, "var", "lock")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	for name, want := range map[string]string{
		"var/lock/iscsi": "run/lock/iscsi", // absolute symlink, read inside the root
		"run/lock/iscsi": "run/lock/iscsi", // no symlink
	} {
		got, err := resolveInRoot(root, name)
		if err != nil {
			t.Fatalf("resolveInRoot(%q) error = %v", name, err)
		}
		if got != want {
			t.Fatalf("resolveInRoot(%q) = %q, want %q", name, got, want)
		}
	}

	// A relative symlink climbing out of the root must still be refused.
	if err := os.Symlink("../../../../etc", filepath.Join(f.root, "run", "escape")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveInRoot(root, "run/escape/iscsi"); err == nil {
		t.Fatalf("resolveInRoot followed a relative symlink out of the root to %q", got)
	}

	// A symlink loop ends in an error, not a hang.
	if err := os.Symlink("/run/loop", filepath.Join(f.root, "run", "loop")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveInRoot(root, "run/loop/iscsi"); err == nil {
		t.Fatalf("resolveInRoot(symlink loop) = %q, want an error", got)
	}
}

// A lock reached only through an absolute symlink inside the host root is still found and
// cleared: the janitor must not depend on the lock directory being free of symlinks.
func TestISCSIDBLockJanitorRemovesLockBehindAbsoluteSymlinkInRoot(t *testing.T) {
	f := newDBLockFixture(t, true)
	// Move the real lock directory to run/lock-real and point var/lock at it the way a
	// distro would (absolute target); run/lock/iscsi no longer exists.
	realDir := filepath.Join(f.root, "run", "lock-real")
	if err := os.Rename(filepath.Join(f.root, "run", "lock"), realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "var"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/run/lock-real", filepath.Join(f.root, "var", "lock")); err != nil {
		t.Fatal(err)
	}
	f.lockDir = filepath.Join(realDir, "iscsi")

	if !f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() = false; the orphaned lock behind /var/lock -> /run/lock-real was not removed")
	}
	if f.writeLockExists(t) {
		t.Fatal("lock.write still present")
	}
}

func TestISCSIDBLockJanitorNilIsNoop(t *testing.T) {
	var j *iscsiDBLockJanitor
	if j.clearStale() {
		t.Fatal("nil janitor reported a removal")
	}
}

func TestHostProcessesVisible(t *testing.T) {
	procWithSelfNS := func(target string) string {
		proc := t.TempDir()
		dir := filepath.Join(proc, "self", "ns")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "pid")); err != nil {
			t.Fatal(err)
		}
		return proc
	}
	if !hostProcessesVisible(procWithSelfNS("pid:[4026531836]")) {
		t.Fatal("the kernel's initial PID namespace (hostPID) must count as visible")
	}
	// shareProcessNamespace without hostPID: PID 1 is the pod's pause container, in its own
	// mount namespace, but the PID namespace is the pod's, not the host's (review finding).
	if hostProcessesVisible(procWithSelfNS("pid:[4026532410]")) {
		t.Fatal("a pod PID namespace must not count as the host's")
	}
	if hostProcessesVisible(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("unreadable namespaces must count as not visible")
	}
}

// iscsistart takes the same lock as iscsiadm and is treated the same way (review finding).
func TestISCSIDBLockJanitorKeepsLockWithOlderLiveIscsistart(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.addProcess(t, 50, "iscsistart", 900*time.Second)
	if f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() removed a lock a live iscsistart may own")
	}
}

// iscsid also takes the lock when it reads or writes records. One blocked in a write (D) or
// running (R) may own it; one asleep in its event loop (S) does not.
func TestISCSIDBLockJanitorKeepsLockWhileIscsidIsBusy(t *testing.T) {
	for _, state := range []string{"D", "R"} {
		f := newDBLockFixture(t, true)
		f.addProcessInState(t, 60, "iscsid", state, 5*time.Second)
		if f.janitor(10 * time.Minute).clearStale() {
			t.Fatalf("clearStale() removed a lock while iscsid was in state %s", state)
		}
	}
}

func TestISCSIDBLockJanitorKeepsLockWhenIscsidStateUnknown(t *testing.T) {
	f := newDBLockFixture(t, true)
	f.writeProc(t, "61/comm", "iscsid\n")
	f.writeProc(t, "61/stat", "garbage")
	if f.janitor(10 * time.Minute).clearStale() {
		t.Fatal("clearStale() removed a lock while iscsid's state was unreadable")
	}
}
