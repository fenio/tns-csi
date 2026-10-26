package driver

import (
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// VolumeContextKeyMkfsOptions carries extra mkfs arguments from the StorageClass to the node.
const VolumeContextKeyMkfsOptions = "mkfsOptions"

// Sub-option keys that name a file or device instead of a size or feature.
const (
	mkfsSuboptFile = "file"
	mkfsSuboptName = "name"
)

// mkfsFlag describes one allowed mkfs option.
type mkfsFlag struct {
	// forbiddenSubopts lists comma-separated sub-option keys (before "=") that are
	// rejected because they point mkfs at another device or a node file.
	forbiddenSubopts []string
	takesArg         bool
}

// mkfsFlags is an allowlist of the options each mkfs accepts here. Anything that names
// a device or reads a file on the node is left out: mkfs runs as root on the node, and
// whoever can create a static PV sets the volume context directly. Positional words are
// rejected because mkfs would take the first one as the device to format.
var (
	mke2fsFlags = map[string]mkfsFlag{
		"-b": {takesArg: true}, "-C": {takesArg: true}, "-E": {takesArg: true},
		"-e": {takesArg: true}, "-G": {takesArg: true}, "-g": {takesArg: true},
		"-I": {takesArg: true}, "-i": {takesArg: true}, "-L": {takesArg: true},
		"-M": {takesArg: true}, "-m": {takesArg: true}, "-N": {takesArg: true},
		"-O": {takesArg: true}, "-r": {takesArg: true}, "-T": {takesArg: true},
		"-J": {takesArg: true, forbiddenSubopts: []string{"device"}},
		"-D": {}, "-j": {}, "-q": {}, "-v": {},
	}
	mkfsXFSFlags = map[string]mkfsFlag{
		"-b": {takesArg: true}, "-i": {takesArg: true}, "-L": {takesArg: true},
		"-m": {takesArg: true}, "-n": {takesArg: true},
		"-d": {takesArg: true, forbiddenSubopts: []string{mkfsSuboptFile, mkfsSuboptName}},
		"-l": {takesArg: true, forbiddenSubopts: []string{mkfsSuboptFile, "logdev", mkfsSuboptName}},
		"-r": {takesArg: true, forbiddenSubopts: []string{mkfsSuboptFile, mkfsSuboptName, "rtdev"}},
		"-K": {}, "-q": {},
	}
)

// validateMkfsOptions checks the mkfsOptions parameter for a volume and returns it
// normalized to single spaces. Empty means no extra options.
func validateMkfsOptions(raw, protocol string, caps []*csi.VolumeCapability) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	if protocol != ProtocolNVMeOF && protocol != ProtocolISCSI {
		return "", status.Error(codes.InvalidArgument, "mkfsOptions is only supported for NVMe-oF and iSCSI volumes")
	}
	for _, capability := range caps {
		mnt := capability.GetMount()
		if mnt == nil {
			return "", status.Error(codes.InvalidArgument, "mkfsOptions requires filesystem volume mode")
		}
		if _, err := mkfsOptionArgs(raw, mnt.GetFsType()); err != nil {
			return "", err
		}
	}
	return strings.Join(strings.Fields(raw), " "), nil
}

// mkfsOptionArgs splits mkfsOptions into arguments for formatting fsType ("" means
// ext4). Options are separated by whitespace; values cannot contain spaces.
func mkfsOptionArgs(raw, fsType string) ([]string, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil, nil
	}

	fsType = strings.ToLower(strings.TrimSpace(fsType))
	var allowed map[string]mkfsFlag
	switch fsType {
	case "", fsTypeExt4, fsTypeExt3:
		allowed = mke2fsFlags
	case fsTypeXFS:
		allowed = mkfsXFSFlags
	default:
		return nil, status.Errorf(codes.InvalidArgument, "mkfsOptions is not supported for filesystem %q", fsType)
	}

	for i := 0; i < len(fields); i++ {
		flag, ok := allowed[fields[i]]
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument,
				"mkfsOptions: unsupported option %q for %s (pass each option and its value as separate words)", fields[i], fsTypeOrDefault(fsType))
		}
		if !flag.takesArg {
			continue
		}
		if i+1 == len(fields) {
			return nil, status.Errorf(codes.InvalidArgument, "mkfsOptions: option %s needs a value", fields[i])
		}
		i++
		for _, subopt := range strings.Split(fields[i], ",") {
			key, _, _ := strings.Cut(subopt, "=")
			for _, forbidden := range flag.forbiddenSubopts {
				if key == forbidden {
					return nil, status.Errorf(codes.InvalidArgument,
						"mkfsOptions: %s %s is not allowed, since it points mkfs at another device or file", fields[i-1], key)
				}
			}
		}
	}
	return fields, nil
}

func fsTypeOrDefault(fsType string) string {
	if fsType == "" {
		return fsTypeExt4
	}
	return fsType
}
