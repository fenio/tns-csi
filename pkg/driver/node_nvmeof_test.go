package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseNVMeListSubsysOutputUsesExactNQN(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	output := []byte(`{
  "Subsystems": [
    {
      "SubsystemNQN": "nqn.2026-02.csi.tns:volume-longer",
      "Paths": [{"Name": "nvme4", "State": "live"}]
    },
    {
      "SubsystemNQN": "nqn.2026-02.csi.tns:volume",
      "Paths": [{"Name": "nvme7", "State": "live"}]
    }
  ]
}`)

	const nqn = "nqn.2026-02.csi.tns:volume"
	if got := service.parseNVMeListSubsysOutputForNQN(output, nqn); got != "/dev/nvme7n1" {
		t.Fatalf("parseNVMeListSubsysOutputForNQN() = %q, want %q", got, "/dev/nvme7n1")
	}
	if got := service.findControllerForNQN(string(output), nqn); got != "nvme7" {
		t.Fatalf("findControllerForNQN() = %q, want %q", got, "nvme7")
	}
}

func TestParseNVMeListSubsysOutputSupportsNQNField(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	output := []byte(`{
  "Subsystems": [{
    "NQN": "nqn.2026-02.csi.tns:volume",
    "Paths": [{"Name": "nvme2", "State": "live"}]
  }]
}`)

	if got := service.parseNVMeListSubsysOutputForNQN(output, "nqn.2026-02.csi.tns:volume"); got != "/dev/nvme2n1" {
		t.Fatalf("parseNVMeListSubsysOutputForNQN() = %q, want %q", got, "/dev/nvme2n1")
	}
}

func TestNVMeNQNPresentUsesExactIdentity(t *testing.T) {
	sysClassPath := t.TempDir()
	writeTestSubsystemNQN(t, sysClassPath, "nvme4", "nqn.2026-02.csi.tns:volume-longer")
	writeTestSubsystemNQN(t, sysClassPath, "nvme4n1", "nqn.2026-02.csi.tns:volume")
	writeTestSubsystemNQN(t, sysClassPath, "nvme7", "nqn.2026-02.csi.tns:volume")

	present, err := nvmeNQNPresent(sysClassPath, "nqn.2026-02.csi.tns:volume")
	if err != nil {
		t.Fatalf("nvmeNQNPresent() unexpected error: %v", err)
	}
	if !present {
		t.Fatal("nvmeNQNPresent() = false, want true")
	}

	present, err = nvmeNQNPresent(sysClassPath, "nqn.2026-02.csi.tns:vol")
	if err != nil {
		t.Fatalf("nvmeNQNPresent() unexpected error: %v", err)
	}
	if present {
		t.Fatal("nvmeNQNPresent() matched a partial NQN")
	}
}

func TestWaitForNVMeNQNRemoval(t *testing.T) {
	sysClassPath := t.TempDir()
	nqnPath := writeTestSubsystemNQN(t, sysClassPath, "nvme4", "nqn.2026-02.csi.tns:volume")
	removed := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		removed <- os.Remove(nqnPath)
	}()

	err := waitForNVMeNQNRemovalAt(context.Background(), sysClassPath, "nqn.2026-02.csi.tns:volume", time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForNVMeNQNRemovalAt() unexpected error: %v", err)
	}
	if removeErr := <-removed; removeErr != nil {
		t.Fatalf("failed to remove test NQN: %v", removeErr)
	}
}

func TestWaitForNVMeNQNRemovalTimesOut(t *testing.T) {
	sysClassPath := t.TempDir()
	writeTestSubsystemNQN(t, sysClassPath, "nvme4", "nqn.2026-02.csi.tns:volume")

	err := waitForNVMeNQNRemovalAt(context.Background(), sysClassPath, "nqn.2026-02.csi.tns:volume", 20*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, ErrNVMeCleanupTimeout) {
		t.Fatalf("waitForNVMeNQNRemovalAt() error = %v, want ErrNVMeCleanupTimeout", err)
	}
}

func TestUnstageNVMeOFVolumeReturnsDisconnectFailure(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	disconnectErr := errors.New("disconnect failed")
	service.disconnectNVMeOFFn = func(_ context.Context, _ string) error {
		return disconnectErr
	}

	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	resp, err := service.unstageNVMeOFVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	}, map[string]string{VolumeContextKeyNQN: "nqn.2026-02.csi.tns:volume"})
	if resp != nil {
		t.Fatalf("unstageNVMeOFVolume() response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("unstageNVMeOFVolume() code = %s, want %s (error: %v)", status.Code(err), codes.Internal, err)
	}
	if got, readErr := readNVMeStagingNQN(stagingPath); readErr != nil || got != "nqn.2026-02.csi.tns:volume" {
		t.Fatalf("staging metadata after failed disconnect = %q, %v", got, readErr)
	}
}

func TestUnstageNVMeOFVolumeRejectsUnknownNQN(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := t.TempDir()

	resp, err := service.unstageNVMeOFVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	}, nil)
	if resp != nil {
		t.Fatalf("unstageNVMeOFVolume() response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("unstageNVMeOFVolume() code = %s, want %s (error: %v)", status.Code(err), codes.Internal, err)
	}
}

func TestNVMeStagingMetadataSupportsUnstageRetry(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	const nqn = "nqn.2026-02.csi.tns:volume"
	if err := writeNVMeStagingNQN(stagingPath, nqn); err != nil {
		t.Fatalf("writeNVMeStagingNQN() unexpected error: %v", err)
	}

	if got := service.detectProtocolFromStagingPath(context.Background(), stagingPath); got != ProtocolNVMeOF {
		t.Fatalf("detectProtocolFromStagingPath() = %q, want %q", got, ProtocolNVMeOF)
	}
	derived, err := service.deriveNQNFromStagingPath(context.Background(), stagingPath)
	if err != nil || derived != nqn {
		t.Fatalf("deriveNQNFromStagingPath() = %q, %v; want %q, nil", derived, err, nqn)
	}

	service.disconnectNVMeOFFn = func(_ context.Context, gotNQN string) error {
		if gotNQN != nqn {
			t.Errorf("disconnect NQN = %q, want %q", gotNQN, nqn)
		}
		return nil
	}
	resp, err := service.unstageNVMeOFVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	}, nil)
	if err != nil || resp == nil {
		t.Fatalf("unstageNVMeOFVolume() = %#v, %v; want non-nil response, nil", resp, err)
	}
	if _, statErr := os.Stat(nvmeStagingMetadataPath(stagingPath)); !os.IsNotExist(statErr) {
		t.Fatalf("staging metadata still exists after successful unstage: %v", statErr)
	}
}

func writeTestSubsystemNQN(t *testing.T, sysClassPath, controller, nqn string) string {
	t.Helper()
	controllerPath := filepath.Join(sysClassPath, controller)
	if err := os.MkdirAll(controllerPath, 0o750); err != nil {
		t.Fatalf("failed to create test controller path: %v", err)
	}
	nqnPath := filepath.Join(controllerPath, "subsysnqn")
	if err := os.WriteFile(nqnPath, []byte(nqn+"\n"), 0o600); err != nil {
		t.Fatalf("failed to write test NQN: %v", err)
	}
	return nqnPath
}
