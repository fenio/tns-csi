//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Unstage must never delete directory contents. If the staging path still holds files
// after the unmount step, they are either the live share (a mount check got it wrong)
// or data that landed on the node's disk; neither is ours to delete. Only an empty
// directory is removed.
func TestUnstageShareVolumesNeverDeleteContents(t *testing.T) {
	service := NewNodeService("test-node", nil, false, nil, false, 5) // testMode=false: real code path

	unstagers := map[string]func(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error){
		"nfs": service.unstageNFSVolume,
		"smb": service.unstageSMBVolume,
	}
	for name, unstage := range unstagers {
		t.Run(name, func(t *testing.T) {
			staging := filepath.Join(t.TempDir(), "globalmount")
			if err := os.Mkdir(staging, 0o750); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(staging, "important.db")
			if err := os.WriteFile(data, []byte("user data"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := unstage(context.Background(), &csi.NodeUnstageVolumeRequest{
				VolumeId:          "pvc-1",
				StagingTargetPath: staging,
			})
			if err != nil {
				t.Fatalf("unstage() error = %v", err)
			}
			if _, statErr := os.Stat(data); statErr != nil {
				t.Fatalf("unstage() deleted file inside staging path: %v", statErr)
			}
		})
	}
}

// rmdir failing with EBUSY means the staging path is still a mount point, i.e. the mount
// check said "not mounted" and was wrong. Reporting success would leave the share
// mounted with nothing left to unmount it; the error makes kubelet retry, and the next
// attempt's mount check drives a real umount.
func TestUnstageShareVolumesFailWhenStagingDirIsStillAMountPoint(t *testing.T) {
	service := NewNodeService("test-node", nil, false, nil, false, 5)

	orig := removeStagingDir
	removeStagingDir = func(name string) error {
		return &os.PathError{Op: "remove", Path: name, Err: syscall.EBUSY}
	}
	t.Cleanup(func() { removeStagingDir = orig })

	unstagers := map[string]func(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error){
		"nfs": service.unstageNFSVolume,
		"smb": service.unstageSMBVolume,
	}
	for name, unstage := range unstagers {
		t.Run(name, func(t *testing.T) {
			staging := filepath.Join(t.TempDir(), "globalmount")
			if err := os.Mkdir(staging, 0o750); err != nil {
				t.Fatal(err)
			}
			_, err := unstage(context.Background(), &csi.NodeUnstageVolumeRequest{
				VolumeId: "pvc-1", StagingTargetPath: staging,
			})
			if status.Code(err) != codes.Internal {
				t.Fatalf("unstage() code = %v (err %v), want Internal so kubelet retries", status.Code(err), err)
			}
		})
	}
}

func TestDetectFilesystemTypeReadsMountTable(t *testing.T) {
	ctx := context.Background()

	fsType, err := detectFilesystemType(ctx, "/")
	if err != nil {
		t.Fatalf("detectFilesystemType(/) error = %v", err)
	}
	if fsType == "" || strings.ContainsAny(fsType, " \n") {
		t.Fatalf("detectFilesystemType(/) = %q, want a single filesystem type", fsType)
	}

	_, err = detectFilesystemType(ctx, t.TempDir())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("detectFilesystemType(non-mount) code = %v, want FailedPrecondition (err %v)", status.Code(err), err)
	}
}

func TestUnstageShareVolumesRemoveEmptyStagingDir(t *testing.T) {
	service := NewNodeService("test-node", nil, false, nil, false, 5)

	staging := filepath.Join(t.TempDir(), "globalmount")
	if err := os.Mkdir(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := service.unstageNFSVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId: "pvc-1", StagingTargetPath: staging,
	}); err != nil {
		t.Fatalf("unstage() error = %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("empty staging dir still present after unstage (stat err = %v)", err)
	}
}
