//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestISCSIUnstageIQNRecoveryWithoutMetadataUsesFindmntFromPATH(t *testing.T) {
	binDir := t.TempDir()
	// Linux mount.IsMounted also uses findmnt. Supply both its TARGET query
	// and the legacy unstage SOURCE query without needing a privileged mount.
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  '-o TARGET -n -l '*) printf '%s\\n' \"$5\" ;;\n" +
		"  '-n -o SOURCE '*) printf '/dev/sdc\\n' ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "findmnt"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
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
	if got := service.resolveISCSIUnstageIQN(context.Background(), stagingPath, nil); got != testISCSIIQN {
		t.Fatalf("recovered IQN = %q, want %q", got, testISCSIIQN)
	}
	if queries != 1 {
		t.Fatalf("session queries = %d, want 1", queries)
	}

	// A failed source lookup must still fail closed rather than guessing an IQN.
	if err := os.WriteFile(filepath.Join(binDir, "findmnt"), []byte("#!/bin/sh\ncase \"$*\" in\n'-o TARGET -n -l '*) printf 'mounted\\n' ;;\n*) exit 1 ;;\nesac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := service.resolveISCSIUnstageIQN(context.Background(), stagingPath, nil); got != "" {
		t.Fatalf("failed source lookup recovered unexpected IQN %q", got)
	}
	if queries != 1 {
		t.Fatal("session query must not run after failed source lookup")
	}
	if _, err := os.Stat(iscsiStagingMetadataPath(stagingPath)); !os.IsNotExist(err) {
		t.Fatalf("recovery unexpectedly wrote staging metadata: %v", err)
	}
}
