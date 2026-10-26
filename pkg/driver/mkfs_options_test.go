package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func mountCapability(fsType string) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsType}},
	}
}

func TestMkfsOptionArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		fsType  string
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: "  ", fsType: fsTypeExt4},
		{name: "ext4 mmp", raw: "-O mmp  -E mmp_update_interval=5", fsType: fsTypeExt4,
			want: []string{"-O", "mmp", "-E", "mmp_update_interval=5"}},
		{name: "default fs is ext4", raw: "-m 0 -q", want: []string{"-m", "0", "-q"}},
		{name: "ext3", raw: "-j -L data", fsType: "EXT3", want: []string{"-j", "-L", "data"}},
		{name: "ext4 journal size", raw: "-J size=64", fsType: fsTypeExt4, want: []string{"-J", "size=64"}},
		{name: "xfs reflink", raw: "-m reflink=1 -l size=64m", fsType: fsTypeXFS,
			want: []string{"-m", "reflink=1", "-l", "size=64m"}},

		{name: "positional word would become the device", raw: "/dev/sda", fsType: fsTypeExt4, wantErr: true},
		{name: "positional after option value", raw: "-O mmp /dev/sda", fsType: fsTypeExt4, wantErr: true},
		{name: "ext4 external journal device", raw: "-J size=64,device=/dev/sdb", fsType: fsTypeExt4, wantErr: true},
		{name: "ext4 populate from node directory", raw: "-d /etc", fsType: fsTypeExt4, wantErr: true},
		{name: "ext4 bad blocks file", raw: "-l /etc/shadow", fsType: fsTypeExt4, wantErr: true},
		{name: "ext4 dry run", raw: "-n", fsType: fsTypeExt4, wantErr: true},
		{name: "ext4 filesystem type override", raw: "-t ext2", fsType: fsTypeExt4, wantErr: true},
		{name: "joined value", raw: "-Ommp", fsType: fsTypeExt4, wantErr: true},
		{name: "missing value", raw: "-O", fsType: fsTypeExt4, wantErr: true},
		{name: "xfs data device", raw: "-d name=/dev/sdb", fsType: fsTypeXFS, wantErr: true},
		{name: "xfs data file", raw: "-d su=64k,file", fsType: fsTypeXFS, wantErr: true},
		{name: "xfs external log", raw: "-l logdev=/dev/sdb,size=64m", fsType: fsTypeXFS, wantErr: true},
		{name: "xfs realtime device", raw: "-r rtdev=/dev/sdb", fsType: fsTypeXFS, wantErr: true},
		{name: "xfs protofile", raw: "-p /etc/proto", fsType: fsTypeXFS, wantErr: true},
		{name: "xfs sector size is set by the driver", raw: "-s size=4096", fsType: fsTypeXFS, wantErr: true},
		{name: "ext option on xfs", raw: "-O mmp", fsType: fsTypeXFS, wantErr: true},
		{name: "unsupported filesystem", raw: "-q", fsType: "btrfs", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mkfsOptionArgs(tt.raw, tt.fsType)
			if tt.wantErr {
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("mkfsOptionArgs(%q, %q) error = %v, want InvalidArgument", tt.raw, tt.fsType, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("mkfsOptionArgs(%q, %q) error = %v", tt.raw, tt.fsType, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("mkfsOptionArgs(%q, %q) = %q, want %q", tt.raw, tt.fsType, got, tt.want)
			}
		})
	}
}

func TestValidateMkfsOptions(t *testing.T) {
	block := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}
	tests := []struct {
		name     string
		raw      string
		protocol string
		want     string
		caps     []*csi.VolumeCapability
		wantErr  bool
	}{
		{name: "unset on nfs", protocol: ProtocolNFS, caps: []*csi.VolumeCapability{mountCapability("")}},
		{name: "iscsi normalized", raw: " -O  mmp ", protocol: ProtocolISCSI,
			caps: []*csi.VolumeCapability{mountCapability(fsTypeExt4)}, want: "-O mmp"},
		{name: "nvmeof xfs", raw: "-K", protocol: ProtocolNVMeOF,
			caps: []*csi.VolumeCapability{mountCapability(fsTypeXFS)}, want: "-K"},
		{name: "nfs", raw: "-O mmp", protocol: ProtocolNFS, caps: []*csi.VolumeCapability{mountCapability("")}, wantErr: true},
		{name: "smb", raw: "-O mmp", protocol: ProtocolSMB, caps: []*csi.VolumeCapability{mountCapability("")}, wantErr: true},
		{name: "block mode", raw: "-O mmp", protocol: ProtocolISCSI, caps: []*csi.VolumeCapability{block}, wantErr: true},
		{name: "invalid for requested fs", raw: "-O mmp", protocol: ProtocolISCSI,
			caps: []*csi.VolumeCapability{mountCapability(fsTypeXFS)}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateMkfsOptions(tt.raw, tt.protocol, tt.caps)
			if tt.wantErr {
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("validateMkfsOptions() error = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("validateMkfsOptions() = %q, %v; want %q, nil", got, err, tt.want)
			}
		})
	}
}

func TestMkfsArgsPutsDeviceLast(t *testing.T) {
	got := mkfsArgs([]string{"-F"}, []string{"-O", "mmp"}, "/dev/sdx")
	if want := []string{"-F", "-O", "mmp", "/dev/sdx"}; !slices.Equal(got, want) {
		t.Errorf("mkfsArgs() = %q, want %q", got, want)
	}
}

func TestHandleDeviceFormattingPassesMkfsArgs(t *testing.T) {
	var gotArgs []string
	service := &NodeService{
		needsFormatFn: func(context.Context, string, bool) (bool, error) { return true, nil },
		formatDeviceFn: func(_ context.Context, _, _, _ string, args []string) error {
			gotArgs = args
			return nil
		},
	}
	want := []string{"-O", "mmp"}
	if _, err := service.handleDeviceFormatting(context.Background(), "volume", "/dev/test", filesystemSpec{fsType: fsTypeExt4, mkfsArgs: want}, "dataset", "target", false); err != nil {
		t.Fatalf("handleDeviceFormatting() error = %v", err)
	}
	if !slices.Equal(gotArgs, want) {
		t.Errorf("format got args %q, want %q", gotArgs, want)
	}
}

func TestNodeStageVolumeRejectsUnsafeMkfsOptions(t *testing.T) {
	service := NewNodeService("node", nil, true, NewNodeRegistry(), false, 1)
	_, err := service.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "tank/csi/pvc-1",
		StagingTargetPath: t.TempDir(),
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsTypeExt4}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
		VolumeContext: map[string]string{
			"protocol":                  ProtocolISCSI,
			VolumeContextKeyMkfsOptions: "-O mmp /dev/sda",
		},
	})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "mkfsOptions") {
		t.Fatalf("NodeStageVolume() error = %v, want InvalidArgument for mkfsOptions", err)
	}
}

// TestFormatDeviceWithMkfsOptions formats a regular file, which mkfs accepts like a
// device. Where the mkfs tool is not installed, it only checks the call fails cleanly.
func TestFormatDeviceWithMkfsOptions(t *testing.T) {
	tests := []struct {
		fsType string
		tool   string
		args   []string
	}{
		{fsType: fsTypeExt4, tool: "mkfs.ext4", args: []string{"-O", "mmp", "-m", "0"}},
		{fsType: fsTypeExt3, tool: "mkfs.ext3", args: []string{"-L", "data"}},
		{fsType: fsTypeXFS, tool: "mkfs.xfs", args: []string{"-m", "reflink=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.fsType, func(t *testing.T) {
			image := filepath.Join(t.TempDir(), "disk.img")
			// mkfs.xfs needs at least 300 MiB; the file is sparse.
			if err := os.WriteFile(image, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(image, 512<<20); err != nil {
				t.Fatal(err)
			}

			err := formatDevice(context.Background(), "test-vol", image, tt.fsType, tt.args)
			if _, lookErr := exec.LookPath(tt.tool); lookErr != nil {
				if err == nil {
					t.Fatalf("formatDevice() succeeded without %s installed", tt.tool)
				}
				t.Skipf("%s not installed: %v", tt.tool, err)
			}
			if err != nil {
				t.Fatalf("formatDevice(%q, %q) error = %v", tt.fsType, tt.args, err)
			}
		})
	}
}
