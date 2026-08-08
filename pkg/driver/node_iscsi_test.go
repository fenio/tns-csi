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

const testISCSIIQN = "iqn.2005-10.org.freenas.ctl:pvc-test-volume"

func TestParseISCSISessionIQNForDevice(t *testing.T) {
	output := `
Target: iqn.2005-10.org.freenas.ctl:pvc-other (non-flash)
    Attached scsi disk sdb          State: running
Target: iqn.2005-10.org.freenas.ctl:pvc-test-volume (non-flash)
    Attached scsi disk sdc          State: running
Target: iqn.2005-10.org.freenas.ctl:pvc-test-volume-longer (non-flash)
    Attached scsi disk sdd          State: running
`

	tests := []struct {
		device string
		want   string
	}{
		{device: "sdb", want: "iqn.2005-10.org.freenas.ctl:pvc-other"},
		{device: "/dev/sdc", want: testISCSIIQN},
		{device: "sdd", want: "iqn.2005-10.org.freenas.ctl:pvc-test-volume-longer"},
		{device: "sde", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.device, func(t *testing.T) {
			if got := parseISCSISessionIQNForDevice(output, tt.device); got != tt.want {
				t.Fatalf("parseISCSISessionIQNForDevice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseISCSISessionDeviceUsesExactIQN(t *testing.T) {
	output := `
Target: iqn.2005-10.org.freenas.ctl:pvc-test-volume-longer (non-flash)
    Attached scsi disk sdb State: running
Target: iqn.2005-10.org.freenas.ctl:pvc-test-volume (non-flash)
    Attached scsi disk sdc State: running
`

	if got := parseISCSISessionDevice(output, testISCSIIQN); got != "sdc" {
		t.Fatalf("parseISCSISessionDevice() = %q, want %q", got, "sdc")
	}
}

func TestFindISCSIIQNForDeviceUsesSessionMetadata(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) != 4 || args[0] != "-m" || args[1] != "session" || args[2] != "-P" || args[3] != "3" {
			t.Fatalf("unexpected iscsiadm arguments: %v", args)
		}
		return []byte("Target: " + testISCSIIQN + " (non-flash)\n    Attached scsi disk sdc State: running\n"), nil
	}

	iqn, err := service.findISCSIIQNForDevice(context.Background(), "/dev/sdc")
	if err != nil || iqn != testISCSIIQN {
		t.Fatalf("findISCSIIQNForDevice() = %q, %v; want %q, nil", iqn, err, testISCSIIQN)
	}
}

func TestFindISCSIIQNForDeviceReturnsSessionErrors(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	queryErr := errors.New("session query failed")
	service.runISCSIAdmFn = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("query output"), queryErr
	}
	if _, err := service.findISCSIIQNForDevice(context.Background(), "/dev/sdc"); !errors.Is(err, queryErr) {
		t.Fatalf("findISCSIIQNForDevice() error = %v, want wrapped query error", err)
	}

	service.runISCSIAdmFn = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("Target: " + testISCSIIQN + "\n    Attached scsi disk sdb State: running\n"), nil
	}
	if _, err := service.findISCSIIQNForDevice(context.Background(), "/dev/sdc"); !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("findISCSIIQNForDevice() error = %v, want ErrISCSIDeviceNotFound", err)
	}
}

func TestISCSISessionPresentInOutputUsesExactIQN(t *testing.T) {
	output := "tcp: [5] 192.168.20.10:3260,1 " + testISCSIIQN + "-longer (non-flash)\n" +
		"tcp: [6] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)\n"

	if !iscsiSessionPresentInOutput(output, testISCSIIQN) {
		t.Fatal("iscsiSessionPresentInOutput() did not find exact IQN")
	}
	if iscsiSessionPresentInOutput(output, "iqn.2005-10.org.freenas.ctl:pvc-test") {
		t.Fatal("iscsiSessionPresentInOutput() matched a partial IQN")
	}
}

func TestLogoutISCSITargetVerifiesExactSessionRemoval(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	commands := 0
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		commands++
		if len(args) >= 2 && args[0] == "-m" && args[1] == "node" {
			return []byte("Logout of session successful"), nil
		}
		if len(args) >= 2 && args[0] == "-m" && args[1] == "session" {
			return []byte("tcp: [4] 192.168.20.10:3260,1 iqn.2005-10.org.freenas.ctl:pvc-other (non-flash)"), nil
		}
		t.Fatalf("unexpected iscsiadm arguments: %v", args)
		return nil, nil
	}

	err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN})
	if err != nil {
		t.Fatalf("logoutISCSITarget() unexpected error: %v", err)
	}
	if commands != 2 {
		t.Fatalf("iscsiadm commands = %d, want 2", commands)
	}
}

func TestLogoutISCSITargetTreatsVerifiedAbsenceAsIdempotent(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	commandErr := errors.New("exit status 21")
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "node" {
			return []byte("No matching sessions found"), commandErr
		}
		return []byte("No active sessions."), commandErr
	}

	if err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN}); err != nil {
		t.Fatalf("logoutISCSITarget() should accept verified session absence: %v", err)
	}
}

func TestLogoutISCSITargetReportsCommandAndVerificationFailure(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	commandErr := errors.New("logout command failed")
	queryErr := errors.New("session query failed")
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "node" {
			return []byte("logout output"), commandErr
		}
		return []byte("query output"), queryErr
	}

	err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN})
	if !errors.Is(err, commandErr) || !errors.Is(err, queryErr) {
		t.Fatalf("logoutISCSITarget() error = %v, want wrapped command and query errors", err)
	}
}

func TestIsISCSISessionPresent(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	service.runISCSIAdmFn = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("tcp: [5] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"), nil
	}
	present, err := service.isISCSISessionPresent(context.Background(), testISCSIIQN)
	if err != nil || !present {
		t.Fatalf("isISCSISessionPresent() = %v, %v; want true, nil", present, err)
	}

	service.runISCSIAdmFn = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("No active sessions."), errors.New("exit status 21")
	}
	present, err = service.isISCSISessionPresent(context.Background(), testISCSIIQN)
	if err != nil || present {
		t.Fatalf("isISCSISessionPresent() = %v, %v; want false, nil", present, err)
	}

	queryErr := errors.New("session query failed")
	service.runISCSIAdmFn = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("unexpected output"), queryErr
	}
	present, err = service.isISCSISessionPresent(context.Background(), testISCSIIQN)
	if present || !errors.Is(err, queryErr) {
		t.Fatalf("isISCSISessionPresent() = %v, %v; want false and wrapped query error", present, err)
	}
}

func TestWaitForISCSISessionLogout(t *testing.T) {
	checks := 0
	err := waitForISCSISessionLogout(
		context.Background(),
		testISCSIIQN,
		time.Second,
		time.Millisecond,
		func(_ context.Context, iqn string) (bool, error) {
			if iqn != testISCSIIQN {
				t.Fatalf("session check IQN = %q, want %q", iqn, testISCSIIQN)
			}
			checks++
			return checks < 3, nil
		},
	)
	if err != nil {
		t.Fatalf("waitForISCSISessionLogout() unexpected error: %v", err)
	}
	if checks != 3 {
		t.Fatalf("session checks = %d, want 3", checks)
	}
}

func TestWaitForISCSISessionLogoutTimesOut(t *testing.T) {
	err := waitForISCSISessionLogout(
		context.Background(),
		testISCSIIQN,
		20*time.Millisecond,
		5*time.Millisecond,
		func(_ context.Context, _ string) (bool, error) { return true, nil },
	)
	if !errors.Is(err, ErrISCSILogoutTimeout) {
		t.Fatalf("waitForISCSISessionLogout() error = %v, want ErrISCSILogoutTimeout", err)
	}
}

func TestWaitForISCSISessionLogoutReturnsQueryError(t *testing.T) {
	queryErr := errors.New("session query failed")
	err := waitForISCSISessionLogout(
		context.Background(),
		testISCSIIQN,
		time.Second,
		time.Millisecond,
		func(_ context.Context, _ string) (bool, error) { return false, queryErr },
	)
	if !errors.Is(err, queryErr) {
		t.Fatalf("waitForISCSISessionLogout() error = %v, want wrapped query error", err)
	}
}

func TestISCSIStagingMetadataSupportsUnstageRetry(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := writeISCSIStagingIQN(stagingPath, testISCSIIQN); err != nil {
		t.Fatalf("writeISCSIStagingIQN() unexpected error: %v", err)
	}

	if got := service.detectProtocolFromStagingPath(context.Background(), stagingPath); got != ProtocolISCSI {
		t.Fatalf("detectProtocolFromStagingPath() = %q, want %q", got, ProtocolISCSI)
	}
	derived, err := service.deriveISCSIIQNFromStagingPath(context.Background(), stagingPath)
	if err != nil || derived != testISCSIIQN {
		t.Fatalf("deriveISCSIIQNFromStagingPath() = %q, %v; want %q, nil", derived, err, testISCSIIQN)
	}

	service.logoutISCSITargetFn = func(_ context.Context, params *iscsiConnectionParams) error {
		if params.iqn != testISCSIIQN {
			t.Errorf("logout IQN = %q, want %q", params.iqn, testISCSIIQN)
		}
		return nil
	}
	resp, err := service.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	})
	if err != nil || resp == nil {
		t.Fatalf("NodeUnstageVolume() = %#v, %v; want non-nil response, nil", resp, err)
	}
	if _, statErr := os.Stat(iscsiStagingMetadataPath(stagingPath)); !os.IsNotExist(statErr) {
		t.Fatalf("staging metadata still exists after successful unstage: %v", statErr)
	}
}

func TestGetStagedISCSIDevicePathRecoversBlockSymlink(t *testing.T) {
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := os.Symlink("/dev/null", stagingPath); err != nil {
		t.Fatalf("failed to create block staging symlink: %v", err)
	}

	devicePath, err := getStagedISCSIDevicePath(context.Background(), stagingPath)
	if err != nil || devicePath != "/dev/null" {
		t.Fatalf("getStagedISCSIDevicePath() = %q, %v; want %q, nil", devicePath, err, "/dev/null")
	}

	nonDevicePath := t.TempDir()
	if _, err := getStagedISCSIDevicePath(context.Background(), nonDevicePath); !errors.Is(err, ErrISCSINonDevicePath) {
		t.Fatalf("getStagedISCSIDevicePath() error = %v, want ErrISCSINonDevicePath", err)
	}
}

func TestInvalidISCSIStagingMetadataStillSelectsISCSI(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := os.WriteFile(iscsiStagingMetadataPath(stagingPath), nil, 0o600); err != nil {
		t.Fatalf("failed to write empty staging metadata: %v", err)
	}

	if _, err := readISCSIStagingIQN(stagingPath); !errors.Is(err, ErrISCSIEmptyIQN) {
		t.Fatalf("readISCSIStagingIQN() error = %v, want ErrISCSIEmptyIQN", err)
	}
	if got := service.detectProtocolFromStagingPath(context.Background(), stagingPath); got != ProtocolISCSI {
		t.Fatalf("detectProtocolFromStagingPath() = %q, want %q", got, ProtocolISCSI)
	}
}

func TestISCSIRawBlockUnstageIsIdempotent(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := os.Symlink("/dev/sdc", stagingPath); err != nil {
		t.Fatalf("failed to create block staging symlink: %v", err)
	}
	if err := writeISCSIStagingIQN(stagingPath, testISCSIIQN); err != nil {
		t.Fatalf("writeISCSIStagingIQN() unexpected error: %v", err)
	}

	logoutCalls := 0
	service.logoutISCSITargetFn = func(_ context.Context, params *iscsiConnectionParams) error {
		logoutCalls++
		if params.iqn != testISCSIIQN {
			t.Errorf("logout IQN = %q, want %q", params.iqn, testISCSIIQN)
		}
		return nil
	}
	req := &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := service.NodeUnstageVolume(context.Background(), req)
		if err != nil || resp == nil {
			t.Fatalf("NodeUnstageVolume() attempt %d = %#v, %v; want non-nil response, nil", attempt, resp, err)
		}
	}
	if logoutCalls != 1 {
		t.Fatalf("logout calls = %d, want 1", logoutCalls)
	}
	if _, err := os.Lstat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("raw block staging symlink still exists after unstage: %v", err)
	}
}

func TestUnstageISCSIVolumeReturnsLogoutFailure(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := filepath.Join(t.TempDir(), "globalmount")
	if err := writeISCSIStagingIQN(stagingPath, testISCSIIQN); err != nil {
		t.Fatalf("writeISCSIStagingIQN() unexpected error: %v", err)
	}
	service.logoutISCSITargetFn = func(_ context.Context, _ *iscsiConnectionParams) error {
		return errors.New("logout failed")
	}

	resp, err := service.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	})
	if resp != nil {
		t.Fatalf("NodeUnstageVolume() response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("NodeUnstageVolume() code = %s, want %s (error: %v)", status.Code(err), codes.Internal, err)
	}
	if got, readErr := readISCSIStagingIQN(stagingPath); readErr != nil || got != testISCSIIQN {
		t.Fatalf("staging metadata after failed logout = %q, %v", got, readErr)
	}
}

func TestUnstageISCSIVolumeRejectsUnknownIQN(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	stagingPath := t.TempDir()

	resp, err := service.unstageISCSIVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "tank/volumes/test-volume",
		StagingTargetPath: stagingPath,
	}, nil)
	if resp != nil {
		t.Fatalf("unstageISCSIVolume() response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("unstageISCSIVolume() code = %s, want %s (error: %v)", status.Code(err), codes.Internal, err)
	}
}
