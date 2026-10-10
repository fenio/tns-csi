//go:build linux

package mount

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	mountutils "k8s.io/mount-utils"
)

// selfMountInfo is the mount table of this process's mount namespace: the same
// view findmnt used, without executing anything or touching the mount points.
const selfMountInfo = "/proc/self/mountinfo"

// MountedEntry reports whether path is a mount point and, if so, what is mounted there.
// When several mounts are stacked on path, the topmost (most recent) one is returned.
//
// It reads the mount table only. It never stats path or runs findmnt, so it cannot hang
// on an unresponsive NFS/SMB server, and an unreadable table is an error rather than
// "not mounted" (callers unmount, re-mount, or remove directories on "not mounted").
func MountedEntry(ctx context.Context, path string) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	target, err := canonicalMountTarget(path)
	if err != nil {
		return Entry{}, false, err
	}
	return lookupMount(selfMountInfo, target)
}

// lookupMount finds the topmost mount whose mount point equals target in mountInfoFile.
func lookupMount(mountInfoFile, target string) (Entry, bool, error) {
	infos, err := mountutils.ParseMountInfo(mountInfoFile)
	if err != nil {
		return Entry{}, false, fmt.Errorf("failed to read mount table %s: %w", mountInfoFile, err)
	}
	target = filepath.Clean(target)
	var (
		entry Entry
		found bool
	)
	// Later lines are mounted on top of earlier ones; keep the last match.
	for i := range infos {
		if filepath.Clean(unescapeMountField(infos[i].MountPoint)) != target {
			continue
		}
		entry = Entry{Source: unescapeMountField(infos[i].Source), FSType: infos[i].FsType}
		found = true
	}
	return entry, found, nil
}

// canonicalMountTarget resolves symlinks in path's parent directories (e.g. a
// /var/lib/kubelet that is a symlink) so the result matches the mount table, which
// always records real paths. The final component is never stat'ed: it may be a mount
// point of an unresponsive server. A missing parent cannot contain a mount point, so
// the cleaned path is returned unchanged and will simply not match.
func canonicalMountTarget(path string) (string, error) {
	clean := filepath.Clean(path)
	parent, base := filepath.Dir(clean), filepath.Base(clean)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return clean, nil
		}
		return "", fmt.Errorf("failed to resolve parent of %s: %w", path, err)
	}
	return filepath.Join(resolved, base), nil
}

// unescapeMountField decodes the octal escapes (\040 space, \011 tab, \012 newline,
// \134 backslash) the kernel uses in /proc/*/mountinfo fields.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		// Escapes are always three octal digits <= \377, so a leading digit above 3 is literal text.
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '3' && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
