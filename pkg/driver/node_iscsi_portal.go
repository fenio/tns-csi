package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

var (
	errISCSIPortalResolution = errors.New("failed to resolve configured iSCSI server")
	errISCSIPortalNotFound   = errors.New("no iSCSI portal matches the configured server")
	errISCSIAmbiguousPortal  = errors.New("multiple iSCSI portals match the configured server")
	errISCSIAmbiguousDevice  = errors.New("multiple iSCSI devices match the configured portal")
)

// resolveISCSIServerIPs resolves once per stage so discovery, login and reuse
// select the same network path even if DNS changes during the operation.
func resolveISCSIServerIPs(ctx context.Context, server string) ([]netip.Addr, error) {
	address := strings.TrimPrefix(strings.TrimSuffix(server, "]"), "[")
	if ip, err := netip.ParseAddr(address); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, server)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", errISCSIPortalResolution, server, err)
	}
	seen := make(map[netip.Addr]bool)
	var ips []netip.Addr
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if ok && !seen[ip.Unmap()] {
			ips = append(ips, ip.Unmap())
			seen[ip.Unmap()] = true
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w %q: no IP addresses returned", errISCSIPortalResolution, server)
	}
	return ips, nil
}

// portalMatchesServer accepts the exact configured address/port, including a
// hostname resolved to the IP advertised by SendTargets. A node record's TPGT
// suffix is not part of the TCP endpoint but is retained for iscsiadm -p.
func portalMatchesServer(portal, server, port string, addresses []netip.Addr) bool {
	endpoint, _, _ := strings.Cut(portal, ",")
	host, portalPort, err := net.SplitHostPort(endpoint)
	if err != nil || portalPort != port {
		return false
	}
	if strings.EqualFold(host, server) {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, expected := range addresses {
		if ip.Unmap() == expected {
			return true
		}
	}
	return false
}

func selectISCSINodePortal(output, iqn, server, port string, addresses []netip.Addr) (string, error) {
	var selected string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != iqn || !portalMatchesServer(fields[0], server, port, addresses) {
			continue
		}
		if selected != "" && selected != fields[0] {
			return "", fmt.Errorf("%w for IQN %s: %s and %s", errISCSIAmbiguousPortal, iqn, selected, fields[0])
		}
		selected = fields[0]
	}
	if selected == "" {
		return "", fmt.Errorf("%w %s:%s for IQN %s", errISCSIPortalNotFound, server, port, iqn)
	}
	return selected, nil
}

// parseISCSISessionDeviceForPortal selects only a session on the configured portal.
// Refuse multiple candidate disks rather than formatting or mounting an
// arbitrarily chosen duplicate device.
func parseISCSISessionDeviceForPortal(output, targetIQN, server, port string, addresses []netip.Addr) (string, error) {
	var selected string
	inTargetSection := false
	portalMatches := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, iscsiTargetPrefix) {
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, iscsiTargetPrefix)))
			inTargetSection = len(fields) > 0 && fields[0] == targetIQN
			portalMatches = false
			continue
		}
		if !inTargetSection {
			continue
		}
		if strings.HasPrefix(line, "Current Portal:") {
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Current Portal:")))
			portalMatches = len(fields) > 0 && portalMatchesServer(fields[0], server, port, addresses)
			continue
		}
		if !portalMatches || !strings.Contains(line, "Attached scsi disk") {
			continue
		}
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "disk" && i+1 < len(fields) {
				if selected != "" && selected != fields[i+1] {
					return "", fmt.Errorf("%w for IQN %s on %s:%s", errISCSIAmbiguousDevice, targetIQN, server, port)
				}
				selected = fields[i+1]
				break
			}
		}
	}
	return selected, nil
}
