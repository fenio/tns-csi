package driver

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

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
			return []byte("192.0.2.10:3260,1 " + testISCSIIQN), nil
		case 2:
			return []byte("100.64.0.5:3260,1 " + testISCSIIQN + "\n192.0.2.10:3260,1 " + testISCSIIQN), nil
		case 3:
			return []byte("Login successful"), nil
		default:
			t.Fatalf("unexpected extra iscsiadm call: %v", args)
			return nil, nil
		}
	}
	if err := service.loginISCSITarget(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	discoveryMatches := len(calls) == 3 && slices.Equal(calls[0], []string{"-m", "discovery", "-t", "sendtargets", "-p", "truenas.local:3260"})
	loginMatches := len(calls) == 3 && slices.Equal(calls[2], []string{"-m", "node", "-T", testISCSIIQN, "-p", "192.0.2.10:3260,1", "--login"})
	if !discoveryMatches || !loginMatches {
		t.Fatalf("iscsiadm calls = %v; login must specify only the LAN portal", calls)
	}

	calls = nil
	service.runISCSIAdmFn = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if len(calls) == 1 {
			return nil, nil
		}
		return []byte("100.64.0.5:3260,1 " + testISCSIIQN), nil
	}
	if err := service.loginISCSITarget(context.Background(), params); !errors.Is(err, errISCSIPortalNotFound) || len(calls) != 2 {
		t.Fatalf("missing configured portal = %v, calls %v; must fail without login", err, calls)
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
