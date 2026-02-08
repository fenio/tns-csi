package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for describe command.
var (
	errNoSharePath    = errors.New("no share path found in properties")
	errNoNFSShare     = errors.New("no NFS share found")
	errNoSubsystemNQN = errors.New("no subsystem NQN found in properties")
)

// Protocol constants.
const (
	protocolNFS    = "nfs"
	protocolNVMeOF = "nvmeof"
	protocolISCSI  = "iscsi"
)

// VolumeDetails contains detailed information about a volume.
//
//nolint:govet // field alignment not critical for CLI output struct
type VolumeDetails struct {
	// Basic info
	Dataset   string `json:"dataset"   yaml:"dataset"`
	VolumeID  string `json:"volumeId"  yaml:"volumeId"`
	Protocol  string `json:"protocol"  yaml:"protocol"`
	Type      string `json:"type"      yaml:"type"` // "dataset" or "zvol"
	MountPath string `json:"mountPath" yaml:"mountPath"`

	// Capacity
	CapacityBytes int64  `json:"capacityBytes" yaml:"capacityBytes"`
	CapacityHuman string `json:"capacityHuman" yaml:"capacityHuman"`
	UsedBytes     int64  `json:"usedBytes"     yaml:"usedBytes"`
	UsedHuman     string `json:"usedHuman"     yaml:"usedHuman"`

	// Metadata
	CreatedAt      string `json:"createdAt"      yaml:"createdAt"`
	DeleteStrategy string `json:"deleteStrategy" yaml:"deleteStrategy"`
	Adoptable      bool   `json:"adoptable"      yaml:"adoptable"`

	// Clone source (if applicable)
	ContentSourceType string `json:"contentSourceType,omitempty" yaml:"contentSourceType,omitempty"`
	ContentSourceID   string `json:"contentSourceId,omitempty"   yaml:"contentSourceId,omitempty"`

	// Clone dependency info (if this is a clone)
	CloneMode      string `json:"cloneMode,omitempty"      yaml:"cloneMode,omitempty"`      // cow, promoted, or detached
	OriginSnapshot string `json:"originSnapshot,omitempty" yaml:"originSnapshot,omitempty"` // ZFS origin for COW clones
	ZFSOrigin      string `json:"zfsOrigin,omitempty"      yaml:"zfsOrigin,omitempty"`      // Actual ZFS origin property

	// NFS-specific (only if protocol is NFS)
	NFSShare *NFSShareDetails `json:"nfsShare,omitempty" yaml:"nfsShare,omitempty"`

	// NVMe-oF-specific (only if protocol is NVMe-oF)
	NVMeOFSubsystem *NVMeOFSubsystemDetails `json:"nvmeofSubsystem,omitempty" yaml:"nvmeofSubsystem,omitempty"`

	// All ZFS properties
	Properties map[string]string `json:"properties" yaml:"properties"`
}

// NFSShareDetails contains NFS share information.
//
//nolint:govet // field alignment not critical for CLI output struct
type NFSShareDetails struct {
	ID      int      `json:"id"      yaml:"id"`
	Path    string   `json:"path"    yaml:"path"`
	Hosts   []string `json:"hosts"   yaml:"hosts"`
	Enabled bool     `json:"enabled" yaml:"enabled"`
}

// NVMeOFSubsystemDetails contains NVMe-oF subsystem information.
//
//nolint:govet // field alignment not critical for CLI output struct
type NVMeOFSubsystemDetails struct {
	ID      int    `json:"id"      yaml:"id"`
	Name    string `json:"name"    yaml:"name"`
	NQN     string `json:"nqn"     yaml:"nqn"`
	Serial  string `json:"serial"  yaml:"serial"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

func newDescribeCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe <volume-id>",
		Short: "Show detailed information about a volume",
		Long: `Show detailed information about a tns-csi managed volume.

The volume can be specified by:
  - CSI volume name (e.g., pvc-12345678-1234-1234-1234-123456789012)
  - Full dataset path (e.g., tank/csi/pvc-12345678-1234-1234-1234-123456789012)

Examples:
  # Describe a volume by CSI name
  kubectl tns-csi describe pvc-12345678-1234-1234-1234-123456789012

  # Describe a volume by dataset path
  kubectl tns-csi describe tank/csi/my-volume

  # Output as YAML
  kubectl tns-csi describe pvc-xxx -o yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd.Context(), args[0], url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runDescribe(ctx context.Context, volumeRef string, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to TrueNAS
	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Find the volume
	details, err := getVolumeDetails(ctx, client, volumeRef)
	if err != nil {
		return err
	}

	// Output based on format
	return outputVolumeDetails(details, *outputFormat)
}

// getVolumeDetails retrieves detailed information about a volume.
func getVolumeDetails(ctx context.Context, client tnsapi.ClientInterface, volumeRef string) (*VolumeDetails, error) {
	var dataset *tnsapi.DatasetWithProperties

	// Try to find by CSI volume name first
	ds, err := client.FindDatasetByCSIVolumeName(ctx, "", volumeRef)
	if err == nil && ds != nil {
		dataset = ds
	} else {
		// Try to find by dataset path - query all managed datasets and filter
		datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
		if err != nil {
			return nil, fmt.Errorf("failed to query datasets: %w", err)
		}
		for i := range datasets {
			if datasets[i].ID == volumeRef {
				dataset = &datasets[i]
				break
			}
		}
	}

	if dataset == nil {
		return nil, fmt.Errorf("%w: %s", errVolumeNotFound, volumeRef)
	}

	// Build details
	details := &VolumeDetails{
		Dataset:    dataset.ID,
		Type:       dataset.Type,
		Properties: make(map[string]string),
	}

	// Extract mount path
	if dataset.Mountpoint != "" {
		details.MountPath = dataset.Mountpoint
	}

	// Extract used space
	if dataset.Used != nil {
		if val, ok := dataset.Used["parsed"].(float64); ok {
			details.UsedBytes = int64(val)
			details.UsedHuman = formatBytes(details.UsedBytes)
		}
	}

	// Extract properties from UserProperties
	for key, prop := range dataset.UserProperties {
		// Store all properties
		details.Properties[key] = prop.Value

		// Extract specific fields
		switch key {
		case tnsapi.PropertyCSIVolumeName:
			details.VolumeID = prop.Value
		case tnsapi.PropertyProtocol:
			details.Protocol = prop.Value
		case tnsapi.PropertyCapacityBytes:
			details.CapacityBytes = tnsapi.StringToInt64(prop.Value)
			details.CapacityHuman = formatBytes(details.CapacityBytes)
		case tnsapi.PropertyCreatedAt:
			details.CreatedAt = prop.Value
		case tnsapi.PropertyDeleteStrategy:
			details.DeleteStrategy = prop.Value
		case tnsapi.PropertyAdoptable:
			details.Adoptable = prop.Value == valueTrue
		case tnsapi.PropertyContentSourceType:
			details.ContentSourceType = prop.Value
		case tnsapi.PropertyContentSourceID:
			details.ContentSourceID = prop.Value
		case tnsapi.PropertyCloneMode:
			details.CloneMode = prop.Value
		case tnsapi.PropertyOriginSnapshot:
			details.OriginSnapshot = prop.Value
		}
	}

	// Get protocol-specific details
	switch details.Protocol {
	case protocolNFS:
		if shareDetails, err := getNFSShareDetails(ctx, client, dataset); err == nil {
			details.NFSShare = shareDetails
		}
	case protocolNVMeOF:
		if subsysDetails, err := getNVMeOFSubsystemDetails(ctx, client, dataset); err == nil {
			details.NVMeOFSubsystem = subsysDetails
		}
	}

	return details, nil
}

// getNFSShareDetails retrieves NFS share details for a dataset.
func getNFSShareDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*NFSShareDetails, error) {
	// Get share path from properties or mountpoint
	sharePath := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if dataset.Mountpoint != "" {
		sharePath = dataset.Mountpoint
	}

	if sharePath == "" {
		return nil, errNoSharePath
	}

	// Query the share
	shares, err := client.QueryNFSShare(ctx, sharePath)
	if err != nil {
		return nil, err
	}

	if len(shares) == 0 {
		return nil, fmt.Errorf("%w for path %s", errNoNFSShare, sharePath)
	}

	share := shares[0]
	return &NFSShareDetails{
		ID:      share.ID,
		Path:    share.Path,
		Hosts:   share.Hosts,
		Enabled: share.Enabled,
	}, nil
}

// getNVMeOFSubsystemDetails retrieves NVMe-oF subsystem details for a dataset.
func getNVMeOFSubsystemDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*NVMeOFSubsystemDetails, error) {
	// Get subsystem NQN from properties
	nqn := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

	if nqn == "" {
		return nil, errNoSubsystemNQN
	}

	// Query the subsystem
	subsystem, err := client.NVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return nil, err
	}

	return &NVMeOFSubsystemDetails{
		ID:      subsystem.ID,
		Name:    subsystem.Name,
		NQN:     subsystem.NQN,
		Serial:  subsystem.Serial,
		Enabled: subsystem.Enabled,
	}, nil
}

// outputVolumeDetails outputs volume details in the specified format.
func outputVolumeDetails(details *VolumeDetails, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(details)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(details)

	case outputFormatTable, "":
		return outputVolumeDetailsTable(details)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// describeKV prints a key-value pair with dimmed key.
func describeKV(key, value string) {
	fmt.Printf("  %s  %s\n", colorMuted.Sprintf("%-18s", key+":"), value)
}

// outputVolumeDetailsTable outputs volume details in table/text format.
func outputVolumeDetailsTable(details *VolumeDetails) error {
	// Header
	colorHeader.Println("=== Volume Details ===") //nolint:errcheck,gosec
	fmt.Println()

	// Basic info
	describeKV("Dataset", details.Dataset)
	describeKV("Volume ID", details.VolumeID)
	describeKV("Protocol", protocolBadge(details.Protocol))
	describeKV("Type", details.Type)
	if details.MountPath != "" {
		describeKV("Mount Path", details.MountPath)
	}
	fmt.Println()

	// Capacity
	colorHeader.Println("=== Capacity ===") //nolint:errcheck,gosec
	describeKV("Provisioned", fmt.Sprintf("%s (%d bytes)", details.CapacityHuman, details.CapacityBytes))
	describeKV("Used", fmt.Sprintf("%s (%d bytes)", details.UsedHuman, details.UsedBytes))
	fmt.Println()

	// Metadata
	colorHeader.Println("=== Metadata ===") //nolint:errcheck,gosec
	describeKV("Created At", details.CreatedAt)
	describeKV("Delete Strategy", details.DeleteStrategy)
	describeKV("Adoptable", strconv.FormatBool(details.Adoptable))
	fmt.Println()

	// Clone info (if this volume was created from a snapshot or volume)
	if details.ContentSourceType != "" || details.CloneMode != "" {
		colorHeader.Println("=== Clone Info ===") //nolint:errcheck,gosec
		if details.ContentSourceType != "" {
			describeKV("Source Type", details.ContentSourceType)
			describeKV("Source ID", details.ContentSourceID)
		}
		if details.CloneMode != "" {
			describeKV("Clone Mode", details.CloneMode)
			switch details.CloneMode {
			case tnsapi.CloneModeCOW:
				describeKV("Dependency", colorError.Sprint("CLONE depends on SNAPSHOT (snapshot cannot be deleted)"))
				if details.OriginSnapshot != "" {
					describeKV("Origin Snapshot", details.OriginSnapshot)
				}
			case tnsapi.CloneModePromoted:
				describeKV("Dependency", colorSuccess.Sprint("SNAPSHOT depends on CLONE (snapshot CAN be deleted)"))
			case tnsapi.CloneModeDetached:
				describeKV("Dependency", colorSuccess.Sprint("None (fully independent copy via send/receive)"))
			}
		}
		fmt.Println()
	}

	// Protocol-specific details
	if details.NFSShare != nil {
		colorHeader.Println("=== NFS Share ===") //nolint:errcheck,gosec
		describeKV("Share ID", strconv.Itoa(details.NFSShare.ID))
		describeKV("Path", details.NFSShare.Path)
		describeKV("Hosts", strings.Join(details.NFSShare.Hosts, ", "))
		describeKV("Enabled", strconv.FormatBool(details.NFSShare.Enabled))
		fmt.Println()
	}

	if details.NVMeOFSubsystem != nil {
		colorHeader.Println("=== NVMe-oF Subsystem ===") //nolint:errcheck,gosec
		describeKV("Subsystem ID", strconv.Itoa(details.NVMeOFSubsystem.ID))
		describeKV("Name", details.NVMeOFSubsystem.Name)
		describeKV("NQN", details.NVMeOFSubsystem.NQN)
		describeKV("Serial", details.NVMeOFSubsystem.Serial)
		describeKV("Enabled", strconv.FormatBool(details.NVMeOFSubsystem.Enabled))
		fmt.Println()
	}

	// All properties
	colorHeader.Println("=== ZFS Properties ===") //nolint:errcheck,gosec

	// Sort property keys for consistent output
	keys := make([]string, 0, len(details.Properties))
	for k := range details.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		describeKV(k, details.Properties[k])
	}

	return nil
}
