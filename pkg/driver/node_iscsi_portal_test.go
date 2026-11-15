package driver

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolveISCSIServerIPs(t *testing.T) {
	//nolint:govet // Field alignment is not relevant for this small test table.
	for _, tt := range []struct {
		server string
		want   netip.Addr
	}{
		{server: "192.0.2.10", want: netip.MustParseAddr("192.0.2.10")},
		{server: "[2001:db8::10]", want: netip.MustParseAddr("2001:db8::10")},
	} {
		t.Run(tt.server, func(t *testing.T) {
			got, err := resolveISCSIServerIPs(context.Background(), tt.server)
			if err != nil || len(got) != 1 || got[0] != tt.want {
				t.Fatalf("resolveISCSIServerIPs() = %v, %v; want [%s]", got, err, tt.want)
			}
		})
	}
	got, err := resolveISCSIServerIPs(context.Background(), "localhost")
	if err != nil || len(got) == 0 {
		t.Fatalf("localhost resolution = %v, %v; want at least one address", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveISCSIServerIPs(ctx, "unresolvable.invalid"); !errors.Is(err, errISCSIPortalResolution) {
		t.Fatalf("canceled lookup error = %v; want portal resolution failure", err)
	}
}

func TestPortalMatchesServerRejectsWrongAddressAndPort(t *testing.T) {
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.10")}
	for _, tt := range []struct {
		portal string
		server string
		port   string
		want   bool
	}{
		{portal: "truenas.local:3260,1", server: "truenas.local", port: "3260", want: true},
		{portal: "192.0.2.10:3260,1", server: "truenas.local", port: "3260", want: true},
		{portal: "100.64.0.5:3260,1", server: "truenas.local", port: "3260"},
		{portal: "192.0.2.10:3261,1", server: "truenas.local", port: "3260"},
		{portal: "not-a-portal", server: "truenas.local", port: "3260"},
	} {
		if got := portalMatchesServer(tt.portal, tt.server, tt.port, ips); got != tt.want {
			t.Errorf("portalMatchesServer(%q) = %v, want %v", tt.portal, got, tt.want)
		}
	}
}

func TestSelectISCSINodePortal(t *testing.T) {
	//nolint:govet // Field alignment is not relevant for this small test table.
	tests := []struct {
		name      string
		server    string
		port      string
		output    string
		ips       []netip.Addr
		want      string
		wantError error
	}{
		{
			name:   "only configured LAN address matches",
			server: "truenas.local", port: "3260", ips: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			output: "100.64.0.5:3260,1 " + testISCSIIQN + "\n" +
				"172.16.0.1:3260,1 " + testISCSIIQN + "\n" +
				"192.0.2.10:3260,1 " + testISCSIIQN + "\n",
			want: "192.0.2.10:3260,1",
		},
		{
			name: "other target and port ignored", server: "192.0.2.10", port: "3260",
			ips: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			output: "192.0.2.10:3260,1 " + testISCSIIQN + "-longer\n" +
				"192.0.2.10:3261,1 " + testISCSIIQN + "\n" +
				"192.0.2.10:3260,1 " + testISCSIIQN + "\n",
			want: "192.0.2.10:3260,1",
		},
		{
			name: "IPv6 portal", server: "2001:db8::10", port: "3260", ips: []netip.Addr{netip.MustParseAddr("2001:db8::10")},
			output: "[2001:db8::10]:3260,1 " + testISCSIIQN + "\n", want: "[2001:db8::10]:3260,1",
		},
		{
			name: "no matching portal refuses to log in", server: "192.0.2.10", port: "3260",
			ips:    []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			output: "100.64.0.5:3260,1 " + testISCSIIQN + "\n", wantError: errISCSIPortalNotFound,
		},
		{
			name: "ambiguous DNS refuses to choose", server: "truenas.local", port: "3260",
			ips: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("100.64.0.5")},
			output: "192.0.2.10:3260,1 " + testISCSIIQN + "\n" +
				"100.64.0.5:3260,1 " + testISCSIIQN + "\n", wantError: errISCSIAmbiguousPortal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectISCSINodePortal(tt.output, testISCSIIQN, tt.server, tt.port, tt.ips)
			if got != tt.want || !errors.Is(err, tt.wantError) {
				t.Fatalf("selectISCSINodePortal() = %q, %v; want %q, %v", got, err, tt.want, tt.wantError)
			}
		})
	}
}

func TestParseISCSISessionDeviceForPortal(t *testing.T) {
	const server = "192.0.2.10"
	ips := []netip.Addr{netip.MustParseAddr(server)}
	wrong := "Target: " + testISCSIIQN + " (non-flash)\n    Current Portal: 100.64.0.5:3260,1\n    Attached scsi disk sdb State: running\n"
	matching := "Target: " + testISCSIIQN + " (non-flash)\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdc State: running\n"
	name, err := parseISCSISessionDeviceForPortal(wrong+matching, testISCSIIQN, server, "3260", ips)
	if err != nil || name != "sdc" {
		t.Fatalf("portal-specific device = %q, %v; want sdc, nil", name, err)
	}
	name, err = parseISCSISessionDeviceForPortal(wrong, testISCSIIQN, server, "3260", ips)
	if err != nil || name != "" {
		t.Fatalf("wrong-portal device = %q, %v; want empty, nil", name, err)
	}
	name, err = parseISCSISessionDeviceForPortal(matching+strings.Replace(matching, "sdc", "sdd", 1), testISCSIIQN, server, "3260", ips)
	if name != "" || !errors.Is(err, errISCSIAmbiguousDevice) {
		t.Fatalf("multiple matching devices = %q, %v; want empty, ambiguity", name, err)
	}
}

func TestLoginISCSITargetSelectsOnePortal(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	params := &iscsiConnectionParams{
		iqn: testISCSIIQN, server: "truenas.local", port: "3260", serverIPs: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
	}
	var calls [][]string
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		switch len(calls) {
		case 1:
			return []byte("100.64.0.5:3260,1 " + testISCSIIQN + "\n192.0.2.10:3260,1 " + testISCSIIQN), nil
		case 2:
			return []byte("Login successful"), nil
		default:
			t.Fatalf("unexpected extra iscsiadm call: %v", args)
			return nil, nil
		}
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	nodeListingMatches := len(calls) == 2 && slices.Equal(calls[0], []string{"-m", "node", "-P", "0"})
	loginMatches := len(calls) == 2 && slices.Equal(calls[1], []string{"-m", "node", "-T", testISCSIIQN, "-p", "192.0.2.10:3260,1", "--login"})
	if !nodeListingMatches || !loginMatches {
		t.Fatalf("iscsiadm calls = %v; login must specify only the LAN portal", calls)
	}

	calls = nil
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if slices.Contains(args, "sendtargets") {
			return nil, nil
		}
		return []byte("100.64.0.5:3260,1 " + testISCSIIQN), nil
	}
	if err := service.loginISCSITarget(context.Background(), params); !errors.Is(err, errISCSIPortalNotFound) || len(calls) != 3 {
		t.Fatalf("missing configured portal = %v, calls %v; must fail without login", err, calls)
	}
}

// A node that already has the target's record must not run a SendTargets discovery: with one
// target per volume it rewrites hundreds of records under open-iscsi's DB lock (#336).
func TestLoginISCSITargetSkipsDiscoveryWhenRecordExists(t *testing.T) {
	service := &NodeService{}
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "sendtargets"):
			t.Fatalf("discovery ran although the node record exists: %v", args)
		case slices.Contains(args, "--login"):
			return []byte("Login successful"), nil
		}
		return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatal(err)
	}
}

func TestLoginISCSITargetDiscoversWhenNoRecordsExist(t *testing.T) {
	service := &NodeService{}
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	var calls []string
	discovered := false
	noRecords := exitError(t, iscsiErrNoObjsFound)
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "sendtargets"):
			calls = append(calls, "discovery")
			discovered = true
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
		case slices.Contains(args, "--login"):
			calls = append(calls, "login")
			return []byte("Login successful"), nil
		}
		calls = append(calls, "list")
		if !discovered {
			// iscsiadm exits 21 (ISCSI_ERR_NO_OBJS_FOUND) on an empty node database.
			return []byte("iscsiadm: No records found"), noRecords
		}
		return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "discovery", "list", "login"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestLoginISCSITargetRefreshesStaleRecordOnce(t *testing.T) {
	service := &NodeService{}
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	var calls []string
	logins := 0
	loginFailed := exitError(t, 24)
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "sendtargets"):
			calls = append(calls, "discovery")
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
		case slices.Contains(args, "--login"):
			calls = append(calls, "login")
			logins++
			if logins == 1 {
				return []byte("iscsiadm: Could not login to [iface: default, target: x]: target not found"), loginFailed
			}
			return []byte("Login successful"), nil
		}
		calls = append(calls, "list")
		return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "login", "discovery", "list", "login"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

// A held DB lock is a local problem; the error must say so and not send the user to TrueNAS.
func TestLoginISCSITargetReportsLockedDatabase(t *testing.T) {
	service := &NodeService{}
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	lockTimeout := exitError(t, 6)
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), lockTimeout
	}
	err := service.loginISCSITarget(context.Background(), params)
	if !errors.Is(err, ErrISCSINodeDBLocked) {
		t.Fatalf("loginISCSITarget() = %v, want ErrISCSINodeDBLocked", err)
	}
	if errors.Is(err, ErrISCSITargetNotInDB) || strings.Contains(err.Error(), "TrueNAS") {
		t.Fatalf("loginISCSITarget() = %v; a local lock must not be reported as a TrueNAS problem", err)
	}
}

// exitError returns a real *exec.ExitError with the given code.
func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("sh exit %d returned no error", code)
	}
	return err
}

func TestLoginISCSITargetUsesCompactListingNotFilteredConfiguration(t *testing.T) {
	// The failure in run 36613808566 showed configuration blocks from a node
	// query with -T. Adding -P 0 to that filtered query would still print blocks.
	configuration := "# BEGIN RECORD 2.1.9\n" +
		"node.name = " + testISCSIIQN + "\n" +
		"node.tpgt = 1\n" +
		"node.conn[0].address = 192.0.2.10\n" +
		"node.conn[0].port = 3260\n# END RECORD\n"
	service := NewNodeService("node", nil, true, nil, false, 5)
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	logins := 0
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "--login"):
			if !slices.Equal(args, []string{"-m", "node", "-T", testISCSIIQN, "-p", "192.0.2.10:3260,1", "--login"}) {
				t.Fatalf("unsafe login arguments: %v", args)
			}
			logins++
			return []byte("Login successful"), nil
		case slices.Contains(args, "sendtargets"):
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
		case slices.Contains(args, "-T"):
			return []byte(configuration), nil
		default:
			if !slices.Equal(args, []string{"-m", "node", "-P", "0"}) {
				t.Fatalf("unexpected node query: %v", args)
			}
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN + "-other\n" +
				"100.64.0.5:3260,1 " + testISCSIIQN + "\n" +
				"192.0.2.10:3260,1 " + testISCSIIQN + "\n"), nil
		}
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatalf("login using compact node listing failed: %v", err)
	}
	if logins != 1 {
		t.Fatalf("login calls = %d, want exactly one", logins)
	}
}

func TestFindISCSIDeviceSkipsWrongPortal(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "truenas.local", port: "3260", serverIPs: []netip.Addr{netip.MustParseAddr("192.0.2.10")}}
	output := "Target: " + testISCSIIQN + "\n    Current Portal: 100.64.0.5:3260,1\n    Attached scsi disk sdb State: running\n" +
		"Target: " + testISCSIIQN + "\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdc State: running\n"
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) { return []byte(output), nil }
	path, err := service.findISCSIDevice(context.Background(), params)
	if err != nil || path != "/dev/sdc" {
		t.Fatalf("findISCSIDevice() = %q, %v; want /dev/sdc", path, err)
	}
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("Target: " + testISCSIIQN + "\n    Current Portal: 100.64.0.5:3260,1\n    Attached scsi disk sdb State: running\n"), nil
	}
	path, err = service.findISCSIDevice(context.Background(), params)
	if path != "" || !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("wrong-portal-only device = %q, %v; want no device", path, err)
	}
}

func TestFindISCSIDeviceRefusesAmbiguousMatchingSessions(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	params := &iscsiConnectionParams{iqn: testISCSIIQN, server: "192.0.2.10", port: "3260"}
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("Target: " + testISCSIIQN + "\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdb State: running\n" +
			"Target: " + testISCSIIQN + "\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdc State: running\n"), nil
	}
	if _, err := service.findISCSIDevice(context.Background(), params); !errors.Is(err, errISCSIAmbiguousDevice) {
		t.Fatalf("findISCSIDevice() error = %v, want ambiguous device", err)
	}
}

func TestStageISCSIVolumeRefusesAmbiguousExistingSession(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	service.runISCSIAdmFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("Target: " + testISCSIIQN + "\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdb State: running\n" +
			"Target: " + testISCSIIQN + "\n    Current Portal: 192.0.2.10:3260,1\n    Attached scsi disk sdc State: running\n"), nil
	}
	request := &csi.NodeStageVolumeRequest{
		VolumeId: "tank/csi/volume", StagingTargetPath: filepath.Join(t.TempDir(), "globalmount"),
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
		VolumeContext:    map[string]string{VolumeContextKeyProtocol: ProtocolISCSI, VolumeContextKeyISCSIIQN: testISCSIIQN, "server": "192.0.2.10", "port": "3260"},
	}
	_, err := service.NodeStageVolume(context.Background(), request)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("NodeStageVolume() error = %v, want FailedPrecondition", err)
	}
}
