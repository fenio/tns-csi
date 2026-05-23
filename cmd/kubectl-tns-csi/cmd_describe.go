package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Protocol constants.
const (
	protocolNFS    = "nfs"
	protocolNVMeOF = "nvmeof"
	protocolISCSI  = "iscsi"
	protocolSMB    = "smb"
)

// Table column headers, CLI tokens, and component names shared across kubectl-tns-csi commands.
const (
	cmdName = "kubectl-tns-csi"

	defaultNamespace = "default"
	summaryName      = "summary"

	colDataset  = "DATASET"
	colProtocol = "PROTOCOL"
	colVolumeID = "VOLUME_ID"
	colType     = "TYPE"

	metaNameKey = "name"

	flagTruenasURL = "truenas-url"
	flagAPIKey     = "api-key"
	keyURL         = "url"
	keyAPIKey      = "apiKey"

	componentTrueNASConnection = "TrueNAS Connection"
	componentNFSShare          = "NFS Share"
	componentSMBShare          = "SMB Share"
	componentNVMeOFSubsystem   = "NVMe-oF Subsystem"
	componentISCSIExtent       = "iSCSI Extent"
)

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
	details, err := dashboard.GetVolumeDetails(ctx, client, volumeRef)
	if err != nil {
		return err
	}

	// Enrich with Kubernetes PV/PVC/Pod data (best-effort, include pods for detail view)
	k8sData := enrichWithK8sData(ctx, true)
	if k8sData.Available {
		if binding := dashboard.MatchK8sBinding(k8sData.Bindings, details.Dataset, details.VolumeID); binding != nil {
			details.K8s = binding
		}
	}

	// Output based on format
	return outputVolumeDetails(details, *outputFormat)
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

	// Kubernetes
	if details.K8s != nil {
		colorHeader.Println("=== Kubernetes ===") //nolint:errcheck,gosec
		describeKV("PV Name", details.K8s.PVName)
		if details.K8s.PVCName != "" {
			describeKV("PVC", fmt.Sprintf("%s/%s", details.K8s.PVCNamespace, details.K8s.PVCName))
		} else {
			describeKV("PVC", colorMuted.Sprint("none"))
		}
		describeKV("PV Status", details.K8s.PVStatus)
		if len(details.K8s.Pods) > 0 {
			describeKV("Pods", strings.Join(details.K8s.Pods, ", "))
		} else {
			describeKV("Pods", colorMuted.Sprint("none"))
		}
		fmt.Println()
	}

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

	if details.SMBShare != nil {
		colorHeader.Println("=== SMB Share ===") //nolint:errcheck,gosec
		describeKV("Share ID", strconv.Itoa(details.SMBShare.ID))
		describeKV("Name", details.SMBShare.Name)
		describeKV("Path", details.SMBShare.Path)
		describeKV("Enabled", strconv.FormatBool(details.SMBShare.Enabled))
		fmt.Println()
	}

	if details.ISCSITarget != nil {
		colorHeader.Println("=== iSCSI Target ===") //nolint:errcheck,gosec
		describeKV("Target ID", strconv.Itoa(details.ISCSITarget.ID))
		describeKV("Name", details.ISCSITarget.Name)
		describeKV("IQN", details.ISCSITarget.IQN)
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
