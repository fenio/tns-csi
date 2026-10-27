package driver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The kubelet calls NodeStageVolume again for volumes that are already mounted
// (after a kubelet restart, or when it is unsure whether the last call completed).
// If the subsystem looks stale at that moment, for example while its controller is
// reconnecting, the reuse path must not disconnect it: the namespace is still
// mounted at the staging path, and disconnecting would leave a dead mount under a
// running pod while a reconnect produces a new device that nothing mounts.
func TestReuseExistingConnectionRefusesToDisconnectMountedStaleDevice(t *testing.T) {
	useFakeSysBlock(t, map[string]string{}, map[string]string{})
	fakeSysClassNVMe(t, map[string]string{"nvme4": nqnA})
	fakeNVMeTools(t, "", errors.New("nvme: command not found"))

	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	origLookup := lookupMountedEntry
	lookupMountedEntry = func(_ context.Context, path string) (mount.Entry, bool, error) {
		if path == stagingPath {
			return mount.Entry{Source: "/dev/nvme4n1", FSType: "ext4"}, true, nil
		}
		return mount.Entry{}, false, nil
	}
	t.Cleanup(func() { lookupMountedEntry = origLookup })

	s := NewNodeService("test-node", nil, false, nil, false, 5)
	disconnects := 0
	s.disconnectNVMeOFFn = func(_ context.Context, _ string) error {
		disconnects++
		return nil
	}

	params := &nvmeOFConnectionParams{nqn: nqnA, server: "truenas.example", port: "4420", transport: "tcp"}
	capability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
	resp, reused, err := s.tryReuseExistingConnection(context.Background(), params, "tank/volumes/vol", stagingPath, capability, false, map[string]string{})

	if reused {
		t.Fatalf("stale device reported as reused")
	}
	if disconnects != 0 {
		t.Fatalf("stale subsystem was disconnected %d time(s) while its namespace is still mounted at %s", disconnects, stagingPath)
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error code = %s, want %s (error: %v)", status.Code(err), codes.Unavailable, err)
	}
}

// When nothing is mounted at the staging path, a stale subsystem is still torn down
// so a full reconnect can follow.
func TestReuseExistingConnectionDisconnectsUnmountedStaleDevice(t *testing.T) {
	useFakeSysBlock(t, map[string]string{}, map[string]string{})
	fakeSysClassNVMe(t, map[string]string{"nvme4": nqnA})
	fakeNVMeTools(t, "", errors.New("nvme: command not found"))

	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	origLookup := lookupMountedEntry
	lookupMountedEntry = func(context.Context, string) (mount.Entry, bool, error) {
		return mount.Entry{}, false, nil
	}
	t.Cleanup(func() { lookupMountedEntry = origLookup })
	origSleep := staleDisconnectSettle
	staleDisconnectSettle = 0
	t.Cleanup(func() { staleDisconnectSettle = origSleep })

	s := NewNodeService("test-node", nil, false, nil, false, 5)
	disconnects := 0
	s.disconnectNVMeOFFn = func(_ context.Context, nqn string) error {
		if nqn != nqnA {
			t.Errorf("disconnect NQN = %q, want %q", nqn, nqnA)
		}
		disconnects++
		return nil
	}

	params := &nvmeOFConnectionParams{nqn: nqnA, server: "truenas.example", port: "4420", transport: "tcp"}
	resp, reused, err := s.tryReuseExistingConnection(context.Background(), params, "tank/volumes/vol", stagingPath, &csi.VolumeCapability{}, false, map[string]string{})
	if err != nil || reused || resp != nil {
		t.Fatalf("got %#v, reused=%v, %v; want nil, false, nil (fall through to a fresh connect)", resp, reused, err)
	}
	if disconnects != 1 {
		t.Fatalf("stale subsystem disconnected %d time(s), want 1", disconnects)
	}
}
