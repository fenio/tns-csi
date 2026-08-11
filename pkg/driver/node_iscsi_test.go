package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
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

func TestParseISCSISessionIDsUsesExactIQN(t *testing.T) {
	output := "tcp: [4] 192.168.20.10:3260,1 " + testISCSIIQN + "-longer (non-flash)\n" +
		"tcp: [59] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)\n" +
		"tcp: [61] 192.168.20.11:3260,1 " + testISCSIIQN + " (non-flash)\n"

	sessionIDs, err := parseISCSISessionIDs(output, testISCSIIQN)
	if err != nil {
		t.Fatalf("parseISCSISessionIDs() unexpected error: %v", err)
	}
	if len(sessionIDs) != 2 || sessionIDs[0] != "59" || sessionIDs[1] != "61" {
		t.Fatalf("parseISCSISessionIDs() = %v, want [59 61]", sessionIDs)
	}
}

func TestParseISCSISessionIDsRejectsInvalidSID(t *testing.T) {
	output := "tcp: [invalid] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"
	if _, err := parseISCSISessionIDs(output, testISCSIIQN); !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("parseISCSISessionIDs() error = %v, want strconv.ErrSyntax", err)
	}
}

func TestLogoutISCSITargetVerifiesExactSessionRemoval(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	var commands atomic.Int32
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		command := commands.Add(1)
		if len(args) == 5 && args[0] == "-m" && args[1] == "session" && args[2] == "-r" && args[4] == "--logout" {
			if args[3] == "59" || args[3] == "61" {
				return []byte("Logout of session successful"), nil
			}
		}
		if len(args) == 2 && args[0] == "-m" && args[1] == "session" && command == 1 {
			return []byte("tcp: [59] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)\n" +
				"tcp: [61] 192.168.20.11:3260,1 " + testISCSIIQN + " (non-flash)"), nil
		}
		if len(args) == 2 && args[0] == "-m" && args[1] == "session" {
			return []byte("tcp: [4] 192.168.20.10:3260,1 iqn.2005-10.org.freenas.ctl:pvc-other (non-flash)"), nil
		}
		t.Fatalf("unexpected iscsiadm arguments: %v", args)
		return nil, nil
	}

	err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN})
	if err != nil {
		t.Fatalf("logoutISCSITarget() unexpected error: %v", err)
	}
	if commands.Load() != 4 {
		t.Fatalf("iscsiadm commands = %d, want 4", commands.Load())
	}
}

func TestLogoutISCSISessionsRunsConcurrently(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	started := make(chan string, 2)
	release := make(chan struct{})
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		started <- args[3]
		<-release
		return []byte("Logout successful"), nil
	}

	result := make(chan error, 1)
	go func() {
		result <- service.logoutISCSISessions(context.Background(), testISCSIIQN, []string{"59", "61"})
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case sessionID := <-started:
			seen[sessionID] = true
		case <-time.After(time.Second):
			t.Fatal("session logout commands did not start concurrently")
		}
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("logoutISCSISessions() unexpected error: %v", err)
	}
	if !seen["59"] || !seen["61"] {
		t.Fatalf("started session logouts = %v, want 59 and 61", seen)
	}
}

func TestLogoutISCSITargetUsesLiveSessionWithoutNodeRecords(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	sessionPresent := true
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 2 && args[0] == "-m" && args[1] == "session" {
			if sessionPresent {
				return []byte("tcp: [59] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"), nil
			}
			return []byte("No active sessions."), errors.New("exit status 21")
		}
		if len(args) == 5 && args[0] == "-m" && args[1] == "session" && args[2] == "-r" && args[3] == "59" && args[4] == "--logout" {
			sessionPresent = false
			return []byte("Logout of session [sid: 59] successful."), nil
		}
		t.Fatalf("unexpected iscsiadm arguments: %v", args)
		return nil, nil
	}

	if err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN}); err != nil {
		t.Fatalf("logoutISCSITarget() unexpected error: %v", err)
	}
}

func TestLogoutISCSITargetReenumeratesLiveSessions(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	queries := 0
	var loggedOut []string
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 2 {
			queries++
			switch queries {
			case 1:
				return []byte("tcp: [59] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"), nil
			case 2:
				return []byte("tcp: [60] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"), nil
			default:
				return []byte("No active sessions."), errors.New("exit status 21")
			}
		}
		if len(args) == 5 && args[0] == "-m" && args[1] == "session" && args[2] == "-r" && args[4] == "--logout" {
			loggedOut = append(loggedOut, args[3])
			return []byte("Logout successful"), nil
		}
		t.Fatalf("unexpected iscsiadm arguments: %v", args)
		return nil, nil
	}

	if err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN}); err != nil {
		t.Fatalf("logoutISCSITarget() unexpected error: %v", err)
	}
	if len(loggedOut) != 2 || loggedOut[0] != "59" || loggedOut[1] != "60" {
		t.Fatalf("logged out sessions = %v, want [59 60]", loggedOut)
	}
}

func TestLogoutISCSITargetReportsTimeout(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.logoutISCSITarget(canceledCtx, &iscsiConnectionParams{iqn: testISCSIIQN})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrISCSILogoutTimeout) {
		t.Fatalf("logoutISCSITarget() cancellation error = %v, want context.Canceled only", err)
	}

	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	err = service.logoutISCSITarget(deadlineCtx, &iscsiConnectionParams{iqn: testISCSIIQN})
	if !errors.Is(err, ErrISCSILogoutTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("logoutISCSITarget() deadline error = %v, want logout timeout wrapping deadline", err)
	}
}

func TestLogoutISCSITargetTreatsVerifiedAbsenceAsIdempotent(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	commandErr := errors.New("exit status 21")
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 2 {
			return []byte("No active sessions."), commandErr
		}
		t.Fatalf("unexpected iscsiadm arguments: %v", args)
		return nil, nil
	}

	if err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN}); err != nil {
		t.Fatalf("logoutISCSITarget() should accept verified session absence: %v", err)
	}
}

func TestLogoutISCSITargetReportsCommandAndVerificationFailure(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	commandErr := errors.New("logout command failed")
	queryErr := errors.New("session query failed")
	queries := 0
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 5 {
			return []byte("logout output"), commandErr
		}
		queries++
		if queries == 1 {
			return []byte("tcp: [59] 192.168.20.10:3260,1 " + testISCSIIQN + " (non-flash)"), nil
		}
		return []byte("query output"), queryErr
	}

	err := service.logoutISCSITarget(context.Background(), &iscsiConnectionParams{iqn: testISCSIIQN})
	if !errors.Is(err, commandErr) || !errors.Is(err, queryErr) {
		t.Fatalf("logoutISCSITarget() error = %v, want wrapped command and query errors", err)
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
