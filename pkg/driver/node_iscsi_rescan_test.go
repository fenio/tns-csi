package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeSysBlockDisk points sysBlockPath at a temp dir holding one disk with the given size in
// 512-byte sectors and an empty device/rescan attribute, and returns the disk's sysfs dir.
func fakeSysBlockDisk(t *testing.T, sectors string) string {
	t.Helper()
	block := t.TempDir()
	orig := sysBlockPath
	sysBlockPath = block
	t.Cleanup(func() { sysBlockPath = orig })
	dev := filepath.Join(block, "sdb")
	if err := os.MkdirAll(filepath.Join(dev, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"size": sectors, "device/rescan": ""} {
		if err := os.WriteFile(filepath.Join(dev, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dev
}

func TestRescanISCSIDevice(t *testing.T) {
	const twoGiB = 2 << 30

	t.Run("triggers a rescan and accepts the new size", func(t *testing.T) {
		dev := fakeSysBlockDisk(t, "4194304\n") // 2 GiB
		if err := rescanISCSIDevice(context.Background(), "/dev/sdb", twoGiB); err != nil {
			t.Fatalf("rescanISCSIDevice() = %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(dev, "device", "rescan")); string(got) != "1" {
			t.Errorf("device/rescan = %q, want \"1\"", got)
		}
	})

	t.Run("waits for the size to change", func(t *testing.T) {
		dev := fakeSysBlockDisk(t, "2097152\n") // 1 GiB until the "kernel" catches up
		go func() {
			time.Sleep(700 * time.Millisecond)
			_ = os.WriteFile(filepath.Join(dev, "size"), []byte("4194304\n"), 0o600)
		}()
		if err := rescanISCSIDevice(context.Background(), "/dev/sdb", twoGiB); err != nil {
			t.Fatalf("rescanISCSIDevice() = %v, want success once the size grows", err)
		}
	})

	t.Run("a disk that stays small is Unavailable, not success", func(t *testing.T) {
		fakeSysBlockDisk(t, "2097152\n")
		orig := iscsiRescanWait
		iscsiRescanWait = 600 * time.Millisecond
		t.Cleanup(func() { iscsiRescanWait = orig })
		err := rescanISCSIDevice(context.Background(), "/dev/sdb", twoGiB)
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("rescanISCSIDevice() = %v, want Unavailable so the kubelet retries", err)
		}
	})

	t.Run("a missing rescan attribute fails", func(t *testing.T) {
		dev := fakeSysBlockDisk(t, "4194304\n")
		if err := os.Remove(filepath.Join(dev, "device", "rescan")); err != nil {
			t.Fatal(err)
		}
		// WriteFile would create it, so make the directory read-only to model sysfs.
		if err := os.Chmod(filepath.Join(dev, "device"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dev, "device"), 0o700) })
		if err := rescanISCSIDevice(context.Background(), "/dev/sdb", twoGiB); status.Code(err) != codes.Internal {
			t.Fatalf("rescanISCSIDevice() = %v, want Internal", err)
		}
	})
}
