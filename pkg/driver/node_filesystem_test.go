package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeFilesystemCheckExitError struct {
	code int
}

func (e fakeFilesystemCheckExitError) Error() string {
	return "filesystem check exited"
}

func (e fakeFilesystemCheckExitError) ExitCode() int {
	return e.code
}

func TestRunFilesystemPreen(t *testing.T) {
	tests := []struct {
		runErr   error
		name     string
		fsType   string
		output   string
		wantCode codes.Code
		wantRuns int
	}{
		{name: "clean ext4", fsType: fsTypeExt4, wantCode: codes.OK, wantRuns: 1},
		{name: "clean ext3", fsType: fsTypeExt3, wantCode: codes.OK, wantRuns: 1},
		{name: "errors corrected", fsType: fsTypeExt4, output: "FILE SYSTEM WAS MODIFIED", runErr: fakeFilesystemCheckExitError{code: 1}, wantCode: codes.OK, wantRuns: 1},
		{name: "reboot required", fsType: fsTypeExt4, runErr: fakeFilesystemCheckExitError{code: 2}, wantCode: codes.FailedPrecondition, wantRuns: 1},
		{name: "errors remain", fsType: fsTypeExt4, runErr: fakeFilesystemCheckExitError{code: 4}, wantCode: codes.FailedPrecondition, wantRuns: 1},
		{name: "operational failure", fsType: fsTypeExt4, runErr: errors.New("executable unavailable"), wantCode: codes.Internal, wantRuns: 1},
		{name: "XFS is rejected", fsType: fsTypeXFS, wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := 0
			service := &NodeService{
				runFilesystemCheckFn: func(_ context.Context, devicePath string) ([]byte, error) {
					runs++
					if devicePath != "/dev/test" {
						t.Fatalf("devicePath = %q, want /dev/test", devicePath)
					}
					return []byte(tt.output), tt.runErr
				},
			}

			err := service.runFilesystemPreen(context.Background(), "/dev/test", tt.fsType)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("runFilesystemPreen() code = %v, want %v (error: %v)", got, tt.wantCode, err)
			}
			if runs != tt.wantRuns {
				t.Fatalf("filesystem check runs = %d, want %d", runs, tt.wantRuns)
			}
		})
	}
}

func TestRunFilesystemPreenCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &NodeService{
		runFilesystemCheckFn: func(context.Context, string) ([]byte, error) {
			t.Fatal("filesystem check must not start for a canceled context")
			return nil, nil
		},
	}

	err := service.runFilesystemPreen(ctx, "/dev/test", fsTypeExt4)
	if status.Code(err) != codes.Canceled {
		t.Fatalf("runFilesystemPreen() code = %v, want Canceled", status.Code(err))
	}
}

func TestCheckFilesystemBeforeMountSkips(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		newlyFormatted bool
	}{
		{name: "disabled", mode: filesystemCheckModeNone},
		{name: "fresh filesystem", mode: filesystemCheckModePreen, newlyFormatted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &NodeService{
				runFilesystemCheckFn: func(context.Context, string) ([]byte, error) {
					t.Fatal("filesystem check must be skipped")
					return nil, nil
				},
			}
			if err := service.checkFilesystemBeforeMount(context.Background(), "/dev/not-present", tt.mode, tt.newlyFormatted); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckFilesystemBeforeMountRefusesMountedDevice(t *testing.T) {
	service := &NodeService{
		isSourceMountedFn: func(context.Context, string) (bool, error) {
			return true, nil
		},
		runFilesystemCheckFn: func(context.Context, string) ([]byte, error) {
			t.Fatal("filesystem check must not run on a mounted device")
			return nil, nil
		},
	}

	err := service.checkFilesystemBeforeMount(context.Background(), "/dev/test", filesystemCheckModePreen, false)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("checkFilesystemBeforeMount() code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestCheckFilesystemBeforeMountFailsClosedOnMountProbeError(t *testing.T) {
	service := &NodeService{
		isSourceMountedFn: func(context.Context, string) (bool, error) {
			return false, errors.New("mount table unavailable")
		},
		runFilesystemCheckFn: func(context.Context, string) ([]byte, error) {
			t.Fatal("filesystem check must not run when mounted state is unknown")
			return nil, nil
		},
	}

	err := service.checkFilesystemBeforeMount(context.Background(), "/dev/test", filesystemCheckModePreen, false)
	if status.Code(err) != codes.Internal {
		t.Fatalf("checkFilesystemBeforeMount() code = %v, want Internal", status.Code(err))
	}
}

func TestCheckFilesystemBeforeMountUsesDefaultMountProbe(t *testing.T) {
	service := &NodeService{}
	err := service.checkFilesystemBeforeMount(context.Background(), "/dev/tns-csi-does-not-exist", filesystemCheckModePreen, false)
	if status.Code(err) != codes.Internal {
		t.Fatalf("checkFilesystemBeforeMount() code = %v, want Internal", status.Code(err))
	}
}

func TestCheckFilesystemBeforeMountRunsPreen(t *testing.T) {
	runs := 0
	service := &NodeService{
		isSourceMountedFn: func(context.Context, string) (bool, error) {
			return false, nil
		},
		detectFilesystemFn: func(context.Context, string) (string, error) {
			return fsTypeExt4, nil
		},
		runFilesystemCheckFn: func(context.Context, string) ([]byte, error) {
			runs++
			return nil, nil
		},
	}

	if err := service.checkFilesystemBeforeMount(context.Background(), "/dev/test", filesystemCheckModePreen, false); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("filesystem check runs = %d, want 1", runs)
	}
}

func TestCheckFilesystemBeforeMountPropagatesDetectionError(t *testing.T) {
	wantErr := status.Error(codes.FailedPrecondition, "detection failed")
	service := &NodeService{
		isSourceMountedFn: func(context.Context, string) (bool, error) {
			return false, nil
		},
		detectFilesystemFn: func(context.Context, string) (string, error) {
			return "", wantErr
		},
	}

	if err := service.checkFilesystemBeforeMount(context.Background(), "/dev/test", filesystemCheckModePreen, false); !errors.Is(err, wantErr) {
		t.Fatalf("checkFilesystemBeforeMount() error = %v, want %v", err, wantErr)
	}
}

func TestEnsureStagingTarget(t *testing.T) {
	stagingPath := filepath.Join(t.TempDir(), "stage")
	mounted, err := ensureStagingTarget(context.Background(), stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	if mounted {
		t.Fatal("new staging target must not be mounted")
	}
	if _, statErr := os.Stat(stagingPath); statErr != nil {
		t.Fatalf("staging target was not created: %v", statErr)
	}

	parentFile := filepath.Join(t.TempDir(), "file")
	if writeErr := os.WriteFile(parentFile, []byte("test"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = ensureStagingTarget(context.Background(), filepath.Join(parentFile, "child"))
	if status.Code(err) != codes.Internal {
		t.Fatalf("ensureStagingTarget() code = %v, want Internal", status.Code(err))
	}
}

func TestDetectBlockFilesystemTypeCommandFailure(t *testing.T) {
	_, err := detectBlockFilesystemType(context.Background(), "/dev/tns-csi-does-not-exist")
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("detectBlockFilesystemType() code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestCheckFilesystemBeforeMountUsesDefaultDetector(t *testing.T) {
	service := &NodeService{
		isSourceMountedFn: func(context.Context, string) (bool, error) {
			return false, nil
		},
	}
	err := service.checkFilesystemBeforeMount(context.Background(), "/dev/tns-csi-does-not-exist", filesystemCheckModePreen, false)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("checkFilesystemBeforeMount() code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestRunE2FSCKCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runE2FSCK(ctx, "/dev/test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runE2FSCK() error = %v, want context canceled", err)
	}
}

func TestRunE2FSCKCapturesCommandFailure(t *testing.T) {
	devicePath := filepath.Join(t.TempDir(), "not-a-filesystem")
	if err := os.WriteFile(devicePath, []byte("not a filesystem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runE2FSCK(context.Background(), devicePath); err == nil {
		t.Fatal("runE2FSCK() error = nil, want command failure")
	}
}

func TestHandleDeviceFormattingOutcomes(t *testing.T) {
	tests := []struct {
		checkErr       error
		name           string
		needsFormat    bool
		wantFormatted  bool
		wantErr        bool
		wantFormatRuns int
	}{
		{name: "existing filesystem", wantFormatted: false},
		{name: "new filesystem", needsFormat: true, wantFormatted: true, wantFormatRuns: 1},
		{name: "detection failure", checkErr: errors.New("detection failed"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatRuns := 0
			service := &NodeService{
				needsFormatFn: func(context.Context, string, bool) (bool, error) {
					return tt.needsFormat, tt.checkErr
				},
				formatDeviceFn: func(context.Context, string, string, string) error {
					formatRuns++
					return nil
				},
			}
			formatted, err := service.handleDeviceFormatting(context.Background(), "volume", "/dev/test", fsTypeExt4, "dataset", "target", false)
			if formatted != tt.wantFormatted {
				t.Fatalf("formatted = %v, want %v", formatted, tt.wantFormatted)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if formatRuns != tt.wantFormatRuns {
				t.Fatalf("format runs = %d, want %d", formatRuns, tt.wantFormatRuns)
			}
		})
	}
}

func TestHandleDeviceFormattingDefaultHooks(t *testing.T) {
	t.Run("default formatter", func(t *testing.T) {
		service := &NodeService{
			needsFormatFn: func(context.Context, string, bool) (bool, error) {
				return false, nil
			},
		}
		formatted, err := service.handleDeviceFormatting(context.Background(), "volume", "/dev/test", fsTypeExt4, "dataset", "target", false)
		if err != nil || formatted {
			t.Fatalf("handleDeviceFormatting() = %v, %v; want false, nil", formatted, err)
		}
	})

	t.Run("default detector", func(t *testing.T) {
		service := &NodeService{
			formatDeviceFn: func(context.Context, string, string, string) error {
				return errors.New("format blocked")
			},
		}
		if _, err := service.handleDeviceFormatting(context.Background(), "volume", "/dev/tns-csi-does-not-exist", fsTypeExt4, "dataset", "target", false); err == nil {
			t.Fatal("handleDeviceFormatting() error = nil, want formatting failure")
		}
	})
}

func TestKeyedMutexSerializesSameKey(t *testing.T) {
	var locks keyedMutex
	unlockFirst, err := locks.lock(context.Background(), "volume")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	done := make(chan struct{})

	go func() {
		unlockSecond, lockErr := locks.lock(context.Background(), "volume")
		if lockErr != nil {
			return
		}
		close(acquired)
		<-releaseSecond
		unlockSecond()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("second lock acquired before first lock was released")
	case <-time.After(25 * time.Millisecond):
	}

	unlockFirst()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second lock did not acquire after first lock was released")
	}
	close(releaseSecond)
	<-done

	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("keyed mutex retained %d entries after release", len(locks.entries))
	}
}

func TestKeyedMutexWaitHonorsContext(t *testing.T) {
	var locks keyedMutex
	unlock, err := locks.lock(context.Background(), "volume")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, lockErr := locks.lock(ctx, "volume"); !errors.Is(lockErr, context.DeadlineExceeded) {
		t.Fatalf("lock() error = %v, want context deadline exceeded", lockErr)
	}
}

func TestKeyedMutexRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var locks keyedMutex
	if _, err := locks.lock(ctx, "volume"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock() error = %v, want context canceled", err)
	}
}

func TestCappedCommandOutput(t *testing.T) {
	var output cappedCommandOutput
	input := []byte(strings.Repeat("x", 10000))
	if written, err := output.Write(input); err != nil || written != len(input) {
		t.Fatalf("Write() = %d, %v; want %d, nil", written, err, len(input))
	}
	if got := len(output.Bytes()); got != 4097 {
		t.Fatalf("captured output length = %d, want 4097", got)
	}
	if written, err := output.Write([]byte("ignored")); err != nil || written != len("ignored") {
		t.Fatalf("second Write() = %d, %v; want %d, nil", written, err, len("ignored"))
	}
}

func TestTruncateCommandOutput(t *testing.T) {
	longOutput := []byte(strings.Repeat("x", 5000))
	got := truncateCommandOutput(longOutput)
	if len(got) >= len(longOutput) || !strings.HasSuffix(got, "... (truncated)") {
		t.Fatalf("truncateCommandOutput() did not truncate output: length=%d", len(got))
	}
}
