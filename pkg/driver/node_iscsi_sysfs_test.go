package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testISCSIIQN = "iqn.2005-10.org.freenas.ctl:pvc-test"

type fakeISCSITree struct {
	root          string
	sessionDir    string
	connectionDir string
	deviceDir     string
}

func newFakeISCSITree(t *testing.T) *fakeISCSITree {
	t.Helper()

	root := t.TempDir()
	tree := &fakeISCSITree{
		root:          root,
		sessionDir:    filepath.Join(root, "sys", "class", "iscsi_session"),
		connectionDir: filepath.Join(root, "sys", "class", "iscsi_connection"),
		deviceDir:     filepath.Join(root, "dev"),
	}
	for _, dir := range []string{tree.sessionDir, tree.connectionDir, tree.deviceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fake iSCSI directory %s: %v", dir, err)
		}
	}
	return tree
}

func (f *fakeISCSITree) addSession(t *testing.T, name, iqn string, lunDevices map[int][]string) {
	t.Helper()

	sessionPath := filepath.Join(f.sessionDir, name)
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionPath, "targetname"), []byte(iqn+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	devicePath := filepath.Join(f.root, "sys", "devices", name)
	for lun, devices := range lunDevices {
		blockPath := filepath.Join(devicePath, "target46:0:0", scsiAddressForLUN(lun), "block")
		if err := os.MkdirAll(blockPath, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, device := range devices {
			if err := os.WriteFile(filepath.Join(blockPath, device), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.deviceDir, device), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := os.Symlink(devicePath, filepath.Join(sessionPath, "device")); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeISCSITree) addConnection(t *testing.T, sessionNumber, address, port string) {
	t.Helper()

	connectionPath := filepath.Join(f.connectionDir, "connection"+sessionNumber+":0")
	if err := os.MkdirAll(connectionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connectionPath, "address"), []byte(address+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connectionPath, "port"), []byte(port+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeISCSITree) config() *iscsiDeviceDiscoveryConfig {
	return &iscsiDeviceDiscoveryConfig{
		validateDevice:     func(string) error { return nil },
		sessionClassDir:    f.sessionDir,
		connectionClassDir: f.connectionDir,
		deviceDir:          f.deviceDir,
	}
}

func scsiAddressForLUN(lun int) string {
	return "46:0:0:" + strconv.Itoa(lun)
}

func TestFindISCSIDeviceInSysfsSelectsExactIdentity(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", "  "+testISCSIIQN+"  ", map[int][]string{
		0:  {"sdae"},
		1:  {"sdaf"},
		12: {"sdag"},
	})

	for _, testCase := range []struct {
		name string
		want string
		lun  int
	}{
		{name: "lun zero", lun: 0, want: "sdae"},
		{name: "multi digit lun", lun: 12, want: "sdag"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := findISCSIDeviceInSysfs(
				context.Background(),
				tree.config(),
				&iscsiConnectionParams{iqn: testISCSIIQN, lun: testCase.lun},
			)
			if err != nil {
				t.Fatalf("findISCSIDeviceInSysfs() error = %v", err)
			}
			if want := filepath.Join(tree.deviceDir, testCase.want); got != want {
				t.Fatalf("findISCSIDeviceInSysfs() = %q, want %q", got, want)
			}
		})
	}
}

func TestFindISCSIDeviceInSysfsRejectsIQNPrefix(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN+"-other", map[int][]string{0: {"sdae"}})

	_, err := findISCSIDeviceInSysfs(
		context.Background(),
		tree.config(),
		&iscsiConnectionParams{iqn: testISCSIIQN, lun: 0},
	)
	if !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("error = %v, want ErrISCSIDeviceNotFound", err)
	}
}

func TestFindISCSIDeviceInSysfsFiltersDuplicateIQNByPortal(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, map[int][]string{0: {"sdae"}})
	tree.addConnection(t, "31", "10.0.70.11", "3260")
	tree.addSession(t, "session32", testISCSIIQN, map[int][]string{0: {"sdaf"}})
	tree.addConnection(t, "32", "10.0.70.10", "3260")

	got, err := findISCSIDeviceInSysfs(
		context.Background(),
		tree.config(),
		&iscsiConnectionParams{
			iqn:    testISCSIIQN,
			server: "10.0.70.10",
			port:   "3260",
			lun:    0,
		},
	)
	if err != nil {
		t.Fatalf("findISCSIDeviceInSysfs() error = %v", err)
	}
	if want := filepath.Join(tree.deviceDir, "sdaf"); got != want {
		t.Fatalf("findISCSIDeviceInSysfs() = %q, want %q", got, want)
	}
}

func TestFindISCSIDeviceInSysfsRejectsAmbiguity(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, map[int][]string{0: {"sdae"}})
	tree.addSession(t, "session32", testISCSIIQN, map[int][]string{0: {"sdaf"}})

	_, err := findISCSIDeviceInSysfs(
		context.Background(),
		tree.config(),
		&iscsiConnectionParams{iqn: testISCSIIQN, lun: 0},
	)
	if !errors.Is(err, ErrISCSIDeviceAmbiguous) {
		t.Fatalf("error = %v, want ErrISCSIDeviceAmbiguous", err)
	}
}

func TestFindISCSIDeviceInSysfsRejectsMultipleDevicesForLUN(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, map[int][]string{0: {"sdae", "sdaf"}})

	_, err := findISCSIDeviceInSysfs(
		context.Background(),
		tree.config(),
		&iscsiConnectionParams{iqn: testISCSIIQN, lun: 0},
	)
	if !errors.Is(err, ErrISCSIDeviceAmbiguous) {
		t.Fatalf("error = %v, want ErrISCSIDeviceAmbiguous", err)
	}
}

func TestFindISCSIDeviceInSysfsFindsDeviceAfterRetryableMiss(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, nil)
	params := &iscsiConnectionParams{iqn: testISCSIIQN, lun: 0}

	if _, err := findISCSIDeviceInSysfs(context.Background(), tree.config(), params); !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("first error = %v, want ErrISCSIDeviceNotFound", err)
	}

	blockPath := filepath.Join(
		tree.root,
		"sys",
		"devices",
		"session31",
		"target46:0:0",
		scsiAddressForLUN(0),
		"block",
	)
	if err := os.MkdirAll(blockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockPath, "sdae"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree.deviceDir, "sdae"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findISCSIDeviceInSysfs(context.Background(), tree.config(), params)
	if err != nil {
		t.Fatalf("second lookup error = %v", err)
	}
	if want := filepath.Join(tree.deviceDir, "sdae"); got != want {
		t.Fatalf("second lookup = %q, want %q", got, want)
	}
}

func TestFindISCSIDeviceInSysfsTreatsDisappearingSessionAsRetryable(t *testing.T) {
	tree := newFakeISCSITree(t)
	sessionPath := filepath.Join(tree.sessionDir, "session31")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionPath, "targetname"), []byte(testISCSIIQN), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tree.root, "missing"), filepath.Join(sessionPath, "device")); err != nil {
		t.Fatal(err)
	}

	_, err := findISCSIDeviceInSysfs(
		context.Background(),
		tree.config(),
		&iscsiConnectionParams{iqn: testISCSIIQN, lun: 0},
	)
	if !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("error = %v, want ErrISCSIDeviceNotFound", err)
	}
}

func TestValidateISCSIBlockDeviceRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-block-device")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateISCSIBlockDevice(path)
	if !errors.Is(err, ErrISCSINotBlockDevice) {
		t.Fatalf("error = %v, want ErrISCSINotBlockDevice", err)
	}
}

func TestStageISCSIVolumeFailsClosedOnAmbiguousDiscovery(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, map[int][]string{0: {"sdae"}})
	tree.addSession(t, "session32", testISCSIIQN, map[int][]string{0: {"sdaf"}})

	commandDir := t.TempDir()
	sentinel := filepath.Join(commandDir, "iscsiadm-invoked")
	script := "#!/bin/sh\n: > \"" + sentinel + "\"\n/bin/sleep 30\n"
	for _, name := range []string{"iscsiadm", "nsenter"} {
		if err := os.WriteFile(filepath.Join(commandDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", commandDir)

	service := NewNodeService("test-node", nil, true, NewNodeRegistry(), false, 1)
	service.iscsiDiscovery = tree.config()
	request := &csi.NodeStageVolumeRequest{
		VolumeId:          "pvc-test",
		StagingTargetPath: filepath.Join(t.TempDir(), "stage"),
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{FsType: fsTypeExt4},
			},
		},
	}
	volumeContext := map[string]string{
		VolumeContextKeyISCSIIQN: testISCSIIQN,
		"server":                 "10.0.70.10",
		"port":                   "3260",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := service.stageISCSIVolume(ctx, request, volumeContext)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status code = %s, want FailedPrecondition; error = %v", status.Code(err), err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("iscsiadm command was invoked; stat error = %v", statErr)
	}
}

func TestWaitForISCSIDeviceFailsClosedOnAmbiguousDiscovery(t *testing.T) {
	tree := newFakeISCSITree(t)
	tree.addSession(t, "session31", testISCSIIQN, map[int][]string{0: {"sdae"}})
	tree.addSession(t, "session32", testISCSIIQN, map[int][]string{0: {"sdaf"}})

	service := NewNodeService("test-node", nil, true, NewNodeRegistry(), false, 1)
	service.iscsiDiscovery = tree.config()
	start := time.Now()

	_, err := service.waitForISCSIDevice(
		context.Background(),
		&iscsiConnectionParams{iqn: testISCSIIQN, lun: 0},
		10*time.Second,
	)
	if !errors.Is(err, ErrISCSIDeviceAmbiguous) {
		t.Fatalf("error = %v, want ErrISCSIDeviceAmbiguous", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fatal discovery error was retried for %v", elapsed)
	}
}
