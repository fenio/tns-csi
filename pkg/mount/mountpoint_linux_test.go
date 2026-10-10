//go:build linux

package mount

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeMountInfo writes a mountinfo fixture and returns its path.
func writeMountInfo(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const fixtureMountInfo = `22 1 0:21 / / rw,relatime - overlay none rw
36 22 0:45 / /var/lib/kubelet/plugins/kubernetes.io/csi/tns/abc/globalmount rw,relatime shared:5 - nfs4 10.0.0.5:/mnt/tank/pvc-1 rw,vers=4.2
37 22 259:2 / /var/lib/kubelet/pods/p/volumes/v/mount rw,relatime - ext4 /dev/nvme0n1 rw
38 37 259:2 / /var/lib/kubelet/pods/p/volumes/v/mount rw,relatime - xfs /dev/nvme1n1 rw
39 22 0:46 / /var/lib/kube\040let/with\040space rw - cifs //nas/share\040one rw
`

func TestLookupMount(t *testing.T) {
	info := writeMountInfo(t, fixtureMountInfo)

	tests := []struct {
		name       string
		target     string
		wantSource string
		wantFSType string
		wantOK     bool
	}{
		{
			name:       "nfs staging mount",
			target:     "/var/lib/kubelet/plugins/kubernetes.io/csi/tns/abc/globalmount",
			wantOK:     true,
			wantSource: "10.0.0.5:/mnt/tank/pvc-1",
			wantFSType: "nfs4",
		},
		{
			name:       "trailing slash is cleaned",
			target:     "/var/lib/kubelet/plugins/kubernetes.io/csi/tns/abc/globalmount/",
			wantOK:     true,
			wantSource: "10.0.0.5:/mnt/tank/pvc-1",
			wantFSType: "nfs4",
		},
		{
			name:       "stacked mounts report the topmost (last) entry",
			target:     "/var/lib/kubelet/pods/p/volumes/v/mount",
			wantOK:     true,
			wantSource: "/dev/nvme1n1",
			wantFSType: "xfs",
		},
		{
			name:       "octal-escaped spaces are decoded",
			target:     "/var/lib/kube let/with space",
			wantOK:     true,
			wantSource: "//nas/share one",
			wantFSType: "cifs",
		},
		{
			name:   "not a mount point",
			target: "/var/lib/kubelet/plugins/kubernetes.io/csi/tns/other/globalmount",
		},
		{
			name:   "parent of a mount point is not itself mounted",
			target: "/var/lib/kubelet/plugins/kubernetes.io/csi/tns/abc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok, err := lookupMount(info, tt.target)
			if err != nil {
				t.Fatalf("lookupMount() error = %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("lookupMount() ok = %v, want %v", ok, tt.wantOK)
			}
			if entry.Source != tt.wantSource || entry.FSType != tt.wantFSType {
				t.Errorf("lookupMount() = %+v, want source %q fstype %q", entry, tt.wantSource, tt.wantFSType)
			}
		})
	}
}

// A mount table that cannot be read must be reported as an error, never as
// "not mounted": callers delete or re-mount on "not mounted".
func TestLookupMountUnreadableTableIsAnError(t *testing.T) {
	if _, _, err := lookupMount(filepath.Join(t.TempDir(), "missing"), "/x"); err == nil {
		t.Fatal("lookupMount() on a missing mount table returned nil error")
	}
	if _, _, err := lookupMount(writeMountInfo(t, "garbage line\n"), "/x"); err == nil {
		t.Fatal("lookupMount() on a malformed mount table returned nil error")
	}
}

func TestCanonicalMountTargetResolvesParentSymlinks(t *testing.T) {
	dir := t.TempDir()
	realParent := filepath.Join(dir, "real-kubelet")
	if err := os.Mkdir(realParent, 0o750); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(dir, "kubelet")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatal(err)
	}

	got, err := canonicalMountTarget(filepath.Join(linkParent, "globalmount"))
	if err != nil {
		t.Fatalf("canonicalMountTarget() error = %v", err)
	}
	if want := filepath.Join(realDir, "globalmount"); got != want {
		t.Errorf("canonicalMountTarget() = %q, want %q", got, want)
	}

	// A missing parent cannot hold a mount point; return the cleaned path unchanged.
	missing := filepath.Join(dir, "nope", "globalmount")
	got, err = canonicalMountTarget(missing + "/")
	if err != nil {
		t.Fatalf("canonicalMountTarget(missing) error = %v", err)
	}
	if got != missing {
		t.Errorf("canonicalMountTarget(missing) = %q, want %q", got, missing)
	}
}

func TestIsMountedAgainstLiveMountTable(t *testing.T) {
	ctx := context.Background()

	mounted, err := IsMounted(ctx, "/")
	if err != nil || !mounted {
		t.Fatalf("IsMounted(/) = %v, %v; want true, nil", mounted, err)
	}

	mounted, err = IsMounted(ctx, t.TempDir())
	if err != nil || mounted {
		t.Fatalf("IsMounted(tempdir) = %v, %v; want false, nil", mounted, err)
	}
}

// Regression: IsMounted used to run findmnt and treat ANY non-zero exit (including
// being killed by its own timeout on a hung NFS server) as "not mounted", after which
// NFS/SMB unstage recursively deleted the staging directory, i.e. the live share.
// IsMounted must not depend on findmnt's exit status at all.
func TestIsMountedDoesNotTrustFindmntExitStatus(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "findmnt")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	mounted, err := IsMounted(context.Background(), "/")
	if err != nil || !mounted {
		t.Fatalf("IsMounted(/) with a failing findmnt = %v, %v; want true, nil", mounted, err)
	}
}

func FuzzLookupMount(f *testing.F) {
	f.Add(fixtureMountInfo, "/var/lib/kube let/with space")
	f.Add("36 22 0:45 / /a\\ rw - nfs4 s rw\n", "/a")
	f.Add("36 22 0:45 / /a\\0 rw - nfs4 s rw\n", "/a")
	f.Fuzz(func(t *testing.T, content, target string) {
		p := filepath.Join(t.TempDir(), "mountinfo")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Skip()
		}
		_, _, _ = lookupMount(p, target) // must not panic
	})
}
