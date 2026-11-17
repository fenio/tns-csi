package driver

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fenio/tns-csi/pkg/metrics"
	"k8s.io/klog/v2"
)

// open-iscsi locks its node database by link("<dir>/lock", "<dir>/lock.write") and releases
// it by unlink() on a clean exit (idbm_lock in usr/idbm.c). It is not an flock, so the kernel
// never drops it: an iscsiadm killed while holding it leaves lock.write behind, and every
// later iscsiadm waits 30 s (3000 x 10 ms) and fails with "Timeout on acquiring lock on DB"
// (#336). The janitor below detects and removes such an orphaned lock.
const (
	iscsiDBWriteLock = "lock.write"
	// iscsiDBLockStaleAfter must stay well above open-iscsi's own 30 s lock wait and any
	// single legitimate hold (iscsid and iscsiadm hold it per record, briefly).
	iscsiDBLockStaleAfter = 60 * time.Second
	// procClockTicks is USER_HZ, the unit of /proc/<pid>/stat starttime. It is 100 on every
	// Linux architecture the driver ships for.
	procClockTicks = 100
)

// iscsiDBLockDirs are the lock directories open-iscsi builds use (LOCK_DIR), relative to
// the host root.
var iscsiDBLockDirs = []string{"run/lock/iscsi", "var/lock/iscsi"}

// iscsiDBLockJanitor removes an orphaned open-iscsi lock.write. Call it only while no
// iscsiadm started by this driver can be running (that is, while holding the node's
// iscsiadm gate), so that any live owner is a process it can see in /proc.
type iscsiDBLockJanitor struct {
	now          func() time.Time
	beforeUnlink func() // test hook
	procDir      string
	roots        []string // host root candidates, e.g. /proc/1/root or /
	staleAfter   time.Duration
}

// newISCSIDBLockJanitor returns a janitor for the host's lock directory, reached through
// /proc/1/root (and the container's /run, which the chart mounts from the host). It returns
// nil, which never removes anything, unless the host's processes are visible: without
// hostPID a live host iscsiadm owning the lock would be invisible to the owner check.
func newISCSIDBLockJanitor() *iscsiDBLockJanitor {
	if !hostProcessesVisible("/proc") {
		klog.Infof("Host processes are not visible (no hostPID); orphaned open-iscsi DB locks will be reported, not removed")
		return nil
	}
	return &iscsiDBLockJanitor{roots: []string{"/proc/1/root", "/"}, procDir: "/proc", staleAfter: iscsiDBLockStaleAfter, now: time.Now}
}

// initialPIDNamespace is how /proc names the kernel's initial PID namespace. Its inode is a
// fixed constant (PROC_PID_INIT_INO, include/linux/proc_ns.h).
const initialPIDNamespace = "pid:[4026531836]"

// hostProcessesVisible reports whether procDir shows every process on the host, i.e. this
// process runs in the kernel's initial PID namespace (hostPID). Comparing PID 1's mount
// namespace with ours is not enough: with shareProcessNamespace, PID 1 is the pod's pause
// container, which also has its own mount namespace, yet /proc shows only the pod. A node
// that is itself a container (kind, k3d) does not qualify either, which only makes the
// janitor report instead of remove.
func hostProcessesVisible(procDir string) bool {
	selfNS, err := os.Readlink(filepath.Join(procDir, "self", "ns", "pid"))
	return err == nil && selfNS == initialPIDNamespace
}

// clearStale removes lock.write when it is old and no iscsiadm that could own it is alive.
// It reports whether it removed a lock. Every doubt (unreadable /proc, unknown process
// age, a lock that changed while being judged) keeps the lock.
func (j *iscsiDBLockJanitor) clearStale() bool {
	if j == nil {
		return false
	}
	seen := make(map[string]bool)
	for _, rootPath := range j.roots {
		if j.clearStaleUnder(rootPath, seen) {
			return true
		}
	}
	return false
}

// clearStaleUnder checks each lock directory under rootPath once. On most distros
// var/lock is a symlink to /run/lock, so both entries of iscsiDBLockDirs name the same
// directory; resolving them first keeps that from being inspected (and reported) twice.
func (j *iscsiDBLockJanitor) clearStaleUnder(rootPath string, seen map[string]bool) bool {
	// os.Root refuses symlinks that escape it, so an absolute symlink under /proc/1/root
	// never redirects us into the container's tree; resolveInRoot reads such a symlink as a
	// host path instead, the way the host itself does.
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		klog.V(2).Infof("open-iscsi DB lock check: cannot open %s: %v", rootPath, err)
		return false
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			klog.V(4).Infof("closing %s: %v", rootPath, closeErr)
		}
	}()

	checked := make(map[string]bool, len(iscsiDBLockDirs))
	for _, dir := range iscsiDBLockDirs {
		resolved, resolveErr := resolveInRoot(root, dir)
		if resolveErr != nil {
			klog.V(2).Infof("open-iscsi DB lock check: cannot resolve %s under %s: %v", dir, rootPath, resolveErr)
			continue
		}
		if checked[resolved] {
			continue
		}
		checked[resolved] = true
		if j.clearStaleIn(root, rootPath, resolved, seen) {
			return true
		}
	}
	return false
}

func (j *iscsiDBLockJanitor) clearStaleIn(root *os.Root, rootPath, dir string, seen map[string]bool) bool {
	lockPath := filepath.Join(dir, iscsiDBWriteLock)
	info, err := root.Lstat(lockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			klog.V(2).Infof("open-iscsi DB lock check: cannot inspect %s under %s: %v", lockPath, rootPath, err)
		}
		return false
	}
	if !info.Mode().IsRegular() {
		klog.Warningf("open-iscsi DB lock %s under %s is not a regular file (%v); leaving it", lockPath, rootPath, info.Mode())
		return false
	}
	ctime, ok := fileChangeTime(info)
	if !ok {
		klog.V(2).Infof("open-iscsi DB lock check: no change time for %s under %s", lockPath, rootPath)
		return false
	}
	// The same lock can be visible through several roots; judge it once.
	key := strconv.FormatInt(ctime.UnixNano(), 10) + "/" + strconv.FormatInt(info.Size(), 10) + "/" + dir
	if seen[key] {
		return false
	}
	seen[key] = true

	age := j.now().Sub(ctime)
	if age < j.staleAfter {
		klog.V(2).Infof("open-iscsi DB lock %s is held (%v old, considered orphaned after %v)", lockPath, age.Round(time.Second), j.staleAfter)
		return false
	}
	owner, scanErr := j.possibleOwner(ctime)
	if scanErr != nil {
		klog.Warningf("open-iscsi DB lock %s is %v old but processes cannot be inspected (%v); leaving it", lockPath, age.Round(time.Second), scanErr)
		return false
	}
	if owner != "" {
		klog.Warningf("open-iscsi DB lock %s is %v old; %s may still own it, leaving it", lockPath, age.Round(time.Second), owner)
		return false
	}

	if j.beforeUnlink != nil {
		j.beforeUnlink()
	}
	// lock.write is a hard link to lock, so the inode cannot tell a re-taken lock apart;
	// the change time can (link and unlink both update it).
	again, err := root.Lstat(lockPath)
	if err != nil {
		return false
	}
	if againCtime, ok := fileChangeTime(again); !ok || !againCtime.Equal(ctime) {
		klog.V(2).Infof("open-iscsi DB lock %s changed while being checked; leaving it", lockPath)
		return false
	}
	if err := root.Remove(lockPath); err != nil {
		klog.Warningf("failed to remove orphaned open-iscsi DB lock %s: %v", lockPath, err)
		return false
	}
	klog.Warningf("Removed orphaned open-iscsi DB lock %s (held for %v with no live iscsiadm owner); "+
		"an iscsiadm was killed while holding it", filepath.Join(rootPath, lockPath), age.Round(time.Second))
	metrics.RecordISCSIStaleDBLockRecovery()
	return true
}

// maxSymlinkHops bounds symlink resolution, like the kernel's MAXSYMLINKS.
const maxSymlinkHops = 40

var (
	errPathAboveRoot   = errors.New("path climbs above the root")
	errTooManySymlinks = errors.New("too many levels of symbolic links")
)

// resolveInRoot resolves the symlinks in name (a path relative to root) the way the host
// would if root were its "/": an absolute target restarts from root rather than from the
// container's "/". A path that climbs above root with ".." is refused. Missing components
// are kept literally, so a lock directory that does not exist yet still resolves.
func resolveInRoot(root *os.Root, name string) (string, error) {
	pending := strings.Split(filepath.ToSlash(name), "/")
	var resolved []string
	hops := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return "", fmt.Errorf("%s: %w", name, errPathAboveRoot)
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		current := filepath.Join(append(resolved, part)...)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			resolved = append(append(resolved, part), pending...)
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, part)
			continue
		}
		hops++
		if hops > maxSymlinkHops {
			return "", fmt.Errorf("%s: %w", name, errTooManySymlinks)
		}
		target, err := root.Readlink(current)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			resolved = nil
		}
		pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
	}
	if len(resolved) == 0 {
		return ".", nil
	}
	return filepath.Join(resolved...), nil
}

// possibleOwner returns a description of a live process that may own the lock, or "" if there
// is none: an iscsiadm or iscsistart that started at or before the lock was taken (one started
// later is only waiting for it), or an iscsid that is not idle. A process whose start time or
// state cannot be read counts as a possible owner.
func (j *iscsiDBLockJanitor) possibleOwner(lockTime time.Time) (string, error) {
	entries, err := os.ReadDir(j.procDir)
	if err != nil {
		return "", err
	}
	bootTime, bootErr := readBootTime(filepath.Join(j.procDir, "stat"))
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join(j.procDir, entry.Name(), "comm"))
		if readErr != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		statPath := filepath.Join(j.procDir, entry.Name(), "stat")
		switch name {
		case "iscsiadm", "iscsistart":
			// Short-lived clients: one started after the lock was taken is only waiting for it.
			if !startedAfter(statPath, bootTime, bootErr, lockTime) {
				return name + " pid " + entry.Name(), nil
			}
		case "iscsid":
			// The daemon also takes the lock to read and write records, but it is always older
			// than the lock, so its age proves nothing; counting it unconditionally would never
			// remove an orphaned lock. Asleep in its event loop (S, I) it holds nothing; blocked
			// in a write (D), running (R) or unreadable, it may.
			if state, idle := processIdle(statPath); !idle {
				return fmt.Sprintf("iscsid pid %s (state %c)", entry.Name(), state), nil
			}
		}
	}
	return "", nil
}

// startedAfter reports whether the process whose stat file is statPath certainly started
// after lockTime. Any read or parse failure answers false (it may be the owner).
func startedAfter(statPath string, bootTime time.Time, bootErr error, lockTime time.Time) bool {
	if bootErr != nil {
		return false
	}
	started, err := readProcessStart(statPath, bootTime)
	// Allow a second of slack: starttime has tick resolution, btime second resolution.
	return err == nil && started.After(lockTime.Add(time.Second))
}

// processIdle reports whether the process is asleep (S) or idle (I), with its state for logs.
// An unreadable state is not idle.
func processIdle(statPath string) (state byte, idle bool) {
	state, err := readProcessState(statPath)
	return state, err == nil && (state == 'S' || state == 'I')
}

// readProcessState returns field 3 (state) of /proc/<pid>/stat.
func readProcessState(path string) (byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return '?', err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return '?', os.ErrInvalid
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) == 0 || len(fields[0]) != 1 {
		return '?', os.ErrInvalid
	}
	return fields[0][0], nil
}

func readBootTime(path string) (time.Time, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return time.Time{}, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "btime" {
			seconds, convErr := strconv.ParseInt(fields[1], 10, 64)
			if convErr != nil {
				return time.Time{}, convErr
			}
			return time.Unix(seconds, 0), nil
		}
	}
	return time.Time{}, os.ErrNotExist
}

// readProcessStart parses field 22 (starttime) of /proc/<pid>/stat. Field 2 (comm) may
// contain spaces and parentheses, so fields are counted after its last ')'.
func readProcessStart(path string, bootTime time.Time) (time.Time, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return time.Time{}, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return time.Time{}, os.ErrInvalid
	}
	fields := strings.Fields(string(data[end+1:]))
	// fields[0] is field 3 (state), so starttime (field 22) is fields[19].
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return time.Time{}, os.ErrInvalid
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return bootTime.Add(time.Duration(ticks) * time.Second / procClockTicks), nil
}
