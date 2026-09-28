package driver

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nfsShareAccess restricts which clients TrueNAS allows to mount a share.
// Empty lists preserve the existing, unrestricted behavior.
type nfsShareAccess struct {
	hosts    []string
	networks []string
}

func parseNFSShareAccess(params map[string]string) (nfsShareAccess, error) {
	hosts, err := parseNFSAccessList(params["nfsHosts"], "nfsHosts", false)
	if err != nil {
		return nfsShareAccess{}, err
	}
	networks, err := parseNFSAccessList(params["nfsNetworks"], "nfsNetworks", true)
	if err != nil {
		return nfsShareAccess{}, err
	}
	return nfsShareAccess{hosts: hosts, networks: networks}, nil
}

func parseNFSAccessList(raw, key string, networks bool) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	values := make([]string, 0, strings.Count(raw, ",")+1)
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, status.Errorf(codes.InvalidArgument, "%s contains an empty entry", key)
		}
		if networks {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "%s entry %q must be a CIDR network: %v", key, value, err)
			}
			value = prefix.Masked().String()
		} else if _, err := netip.ParseAddr(value); err != nil && !validNFSHostname(value) {
			return nil, status.Errorf(codes.InvalidArgument, "%s entry %q must be an IP address or hostname", key, value)
		}
		if !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values, nil
}

func validNFSHostname(host string) bool {
	host = strings.TrimSuffix(host, ".") // Accept fully qualified names.
	if host == "" || len(host) > 253 {
		return false
	}
	// A dotted numeric address with an invalid octet must not pass as a hostname.
	numericOnly := strings.IndexFunc(host, func(char rune) bool {
		return char != '.' && (char < '0' || char > '9')
	}) == -1
	if strings.Contains(host, ".") && numericOnly {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			switch {
			case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-':
			default:
				return false
			}
		}
	}
	return true
}

// Existing shares are not changed implicitly. If a caller explicitly requests
// restrictions, fail rather than claiming the existing share has those limits.
func checkExistingNFSShareAccess(access nfsShareAccess, share *tnsapi.NFSShare) error {
	if len(access.hosts) == 0 && len(access.networks) == 0 {
		return nil
	}
	if !sameNFSAccessEntries(access.hosts, share.Hosts) || !sameNFSAccessEntries(access.networks, share.Networks) {
		return status.Errorf(codes.FailedPrecondition,
			"NFS share %d at %s does not match requested nfsHosts/nfsNetworks; update the existing share in TrueNAS before retrying",
			share.ID, share.Path)
	}
	return nil
}

func sameNFSAccessEntries(expected, actual []string) bool {
	left := slices.Clone(expected)
	right := slices.Clone(actual)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
