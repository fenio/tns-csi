package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fenio/tns-csi/pkg/mount"
)

// stubMountedEntry replaces the mount-table lookup for the duration of a test.
func stubMountedEntry(t *testing.T, fn func(context.Context, string) (mount.Entry, bool, error)) {
	t.Helper()
	orig := lookupMountedEntry
	lookupMountedEntry = fn
	t.Cleanup(func() { lookupMountedEntry = orig })
}

// Legacy staged volumes have no metadata file; the IQN is recovered from the device
// mounted at the staging path. The lookup must use the mount table, and any failure
// to read it must fail closed rather than guess an IQN (which could log out a
// different volume's session).
func TestISCSIUnstageIQNRecoveryWithoutMetadataUsesMountTable(t *testing.T) {
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(iscsiStagingMetadataPath(stagingPath)); !os.IsNotExist(err) {
		t.Fatalf("expected missing legacy staging metadata, got %v", err)
	}

	service := NewNodeService("test-node", nil, true, nil, false, 5)
	queries := 0
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if !slices.Equal(args, []string{"-m", "session", "-P", "3"}) {
			t.Fatalf("unexpected iSCSI query: %v", args)
		}
		queries++
		return []byte("Target: " + testISCSIIQN + "\n\tAttached scsi disk sdc State: running\n"), nil
	}

	stubMountedEntry(t, func(_ context.Context, path string) (mount.Entry, bool, error) {
		if path != stagingPath {
			t.Fatalf("mount lookup for %q, want %q", path, stagingPath)
		}
		return mount.Entry{Source: "/dev/sdc", FSType: "ext4"}, true, nil
	})
	if got := service.resolveISCSIUnstageIQN(context.Background(), stagingPath, nil); got != testISCSIIQN {
		t.Fatalf("recovered IQN = %q, want %q", got, testISCSIIQN)
	}
	if queries != 1 {
		t.Fatalf("session queries = %d, want 1", queries)
	}

	// An unreadable mount table must fail closed rather than guessing an IQN.
	stubMountedEntry(t, func(context.Context, string) (mount.Entry, bool, error) {
		return mount.Entry{}, false, errors.New("mount table unreadable")
	})
	if got := service.resolveISCSIUnstageIQN(context.Background(), stagingPath, nil); got != "" {
		t.Fatalf("failed mount lookup recovered unexpected IQN %q", got)
	}
	if queries != 1 {
		t.Fatal("session query must not run after a failed mount lookup")
	}
	if _, err := os.Stat(iscsiStagingMetadataPath(stagingPath)); !os.IsNotExist(err) {
		t.Fatalf("recovery unexpectedly wrote staging metadata: %v", err)
	}
}
