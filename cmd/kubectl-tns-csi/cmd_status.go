package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for status command.
var (
	errVolumeNotFound        = errors.New("volume not found")
	errNFSShareIDNotFound    = errors.New("NFS share ID not found in properties")
	errNFSShareDisabled      = errors.New("NFS share is disabled")
	errNFSShareNotFound      = errors.New("NFS share not found")
	errNVMeSubsystemNotFound = errors.New("NVMe-oF subsystem not found")
	errNVMeNamespaceNotFound = errors.New("NVMe-oF namespace not found")
	errStatusUnknownFormat   = errors.New("unknown output format")
)

// VolumeStatus represents detailed status of a volume.
type VolumeStatus struct {
	VolumeID string `json:"volumeId" yaml:"volumeId"`
	Dataset  string `json:"dataset" yaml:"dataset"`
	Protocol string `json:"protocol" yaml:"protocol"`
	Type     string `json:"type" yaml:"type"`

	// Capacity info
	CapacityBytes  int64  `json:"capacityBytes" yaml:"capacityBytes"`
	CapacityHuman  string `json:"capacityHuman" yaml:"capacityHuman"`
	UsedBytes      int64  `json:"usedBytes,omitempty" yaml:"usedBytes,omitempty"`
	UsedHuman      string `json:"usedHuman,omitempty" yaml:"usedHuman,omitempty"`
	AvailableBytes int64  `json:"availableBytes,omitempty" yaml:"availableBytes,omitempty"`
	AvailableHuman string `json:"availableHuman,omitempty" yaml:"availableHuman,omitempty"`
	UsedPercent    string `json:"usedPercent,omitempty" yaml:"usedPercent,omitempty"`

	// NFS-specific
	NFSShareID   int    `json:"nfsShareId,omitempty" yaml:"nfsShareId,omitempty"`
	NFSSharePath string `json:"nfsSharePath,omitempty" yaml:"nfsSharePath,omitempty"`
	NFSEnabled   bool   `json:"nfsEnabled,omitempty" yaml:"nfsEnabled,omitempty"`

	// NVMe-oF-specific
	NVMeSubsystemID int    `json:"nvmeSubsystemId,omitempty" yaml:"nvmeSubsystemId,omitempty"`
	NVMeNamespaceID int    `json:"nvmeNamespaceId,omitempty" yaml:"nvmeNamespaceId,omitempty"`
	NVMeNQN         string `json:"nvmeNqn,omitempty" yaml:"nvmeNqn,omitempty"`

	// Health
	Healthy bool     `json:"healthy" yaml:"healthy"`
	Issues  []string `json:"issues,omitempty" yaml:"issues,omitempty"`
}

func newStatusCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <volume-id>",
		Short: "Show detailed status of a volume from TrueNAS",
		Long: `Show detailed status of a tns-csi managed volume, including:
  - Dataset information (capacity, used space)
  - NFS share status (enabled, path)
  - NVMe-oF subsystem/namespace status
  - Health check results

Examples:
  # Get status of a specific volume
  kubectl tns-csi status pvc-abc123

  # Output as JSON for scripting
  kubectl tns-csi status pvc-abc123 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			volumeID := args[0]
			return runStatus(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, volumeID)
		},
	}
	return cmd
}

func runStatus(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, volumeID string) error {
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

	// Find the volume by CSI volume name
	dataset, err := client.FindDatasetByCSIVolumeName(ctx, "", volumeID)
	if err != nil {
		return fmt.Errorf("failed to find volume: %w", err)
	}
	if dataset == nil {
		return fmt.Errorf("%w: %s", errVolumeNotFound, volumeID)
	}

	// Build status
	status, err := buildVolumeStatus(ctx, client, dataset, volumeID)
	if err != nil {
		return fmt.Errorf("failed to get volume status: %w", err)
	}

	// Output
	return outputStatus(status, *outputFormat)
}

func buildVolumeStatus(ctx context.Context, client tnsapi.ClientInterface, ds *tnsapi.DatasetWithProperties, volumeID string) (*VolumeStatus, error) {
	props := ds.UserProperties

	status := &VolumeStatus{
		VolumeID: volumeID,
		Dataset:  ds.ID,
		Type:     ds.Type,
		Healthy:  true,
	}

	// Extract protocol
	if prop, ok := props[tnsapi.PropertyProtocol]; ok {
		status.Protocol = prop.Value
	}

	// Extract capacity from properties
	if prop, ok := props[tnsapi.PropertyCapacityBytes]; ok {
		status.CapacityBytes = tnsapi.StringToInt64(prop.Value)
		status.CapacityHuman = formatBytes(status.CapacityBytes)
	}

	// Try to get used/available from dataset info
	// This requires additional API calls or parsing dataset response

	// Check protocol-specific resources
	switch status.Protocol {
	case tnsapi.ProtocolNFS:
		if err := checkNFSStatus(ctx, client, ds, props, status); err != nil {
			status.Healthy = false
			status.Issues = append(status.Issues, err.Error())
		}

	case tnsapi.ProtocolNVMeOF:
		if err := checkNVMeOFStatus(ctx, client, ds, props, status); err != nil {
			status.Healthy = false
			status.Issues = append(status.Issues, err.Error())
		}
	}

	return status, nil
}

func checkNFSStatus(ctx context.Context, client tnsapi.ClientInterface, ds *tnsapi.DatasetWithProperties, props map[string]tnsapi.UserProperty, status *VolumeStatus) error {
	// Get NFS share ID from properties
	if prop, ok := props[tnsapi.PropertyNFSShareID]; ok {
		status.NFSShareID = tnsapi.StringToInt(prop.Value)
	}
	if prop, ok := props[tnsapi.PropertyNFSSharePath]; ok {
		status.NFSSharePath = prop.Value
	}

	if status.NFSShareID == 0 {
		return errNFSShareIDNotFound
	}

	// Query NFS shares to verify share exists and is enabled
	shares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to query NFS shares: %w", err)
	}

	for _, share := range shares {
		if share.ID == status.NFSShareID {
			status.NFSEnabled = share.Enabled
			if status.NFSSharePath == "" {
				status.NFSSharePath = share.Path
			}
			if !share.Enabled {
				return fmt.Errorf("%w: %d", errNFSShareDisabled, share.ID)
			}
			return nil
		}
	}

	return fmt.Errorf("%w: %d", errNFSShareNotFound, status.NFSShareID)
}

func checkNVMeOFStatus(ctx context.Context, client tnsapi.ClientInterface, ds *tnsapi.DatasetWithProperties, props map[string]tnsapi.UserProperty, status *VolumeStatus) error {
	// Get NVMe-oF IDs from properties
	if prop, ok := props[tnsapi.PropertyNVMeSubsystemID]; ok {
		status.NVMeSubsystemID = tnsapi.StringToInt(prop.Value)
	}
	if prop, ok := props[tnsapi.PropertyNVMeNamespaceID]; ok {
		status.NVMeNamespaceID = tnsapi.StringToInt(prop.Value)
	}
	if prop, ok := props[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		status.NVMeNQN = prop.Value
	}

	// Verify subsystem exists
	if status.NVMeSubsystemID > 0 {
		subsystems, err := client.ListAllNVMeOFSubsystems(ctx)
		if err != nil {
			return fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
		}

		found := false
		for _, subsys := range subsystems {
			if subsys.ID == status.NVMeSubsystemID {
				found = true
				if status.NVMeNQN == "" {
					status.NVMeNQN = subsys.NQN
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %d", errNVMeSubsystemNotFound, status.NVMeSubsystemID)
		}
	}

	// Verify namespace exists
	if status.NVMeNamespaceID > 0 {
		namespaces, err := client.QueryAllNVMeOFNamespaces(ctx)
		if err != nil {
			return fmt.Errorf("failed to query NVMe-oF namespaces: %w", err)
		}

		found := false
		for _, ns := range namespaces {
			if ns.ID == status.NVMeNamespaceID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %d", errNVMeNamespaceNotFound, status.NVMeNamespaceID)
		}
	}

	return nil
}

func outputStatus(status *VolumeStatus, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)

	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(status)

	case "table", "":
		fmt.Printf("Volume:     %s\n", status.VolumeID)
		fmt.Printf("Dataset:    %s\n", status.Dataset)
		fmt.Printf("Protocol:   %s\n", status.Protocol)
		fmt.Printf("Type:       %s\n", status.Type)
		fmt.Printf("Capacity:   %s\n", status.CapacityHuman)

		if status.Protocol == tnsapi.ProtocolNFS {
			fmt.Printf("\nNFS Status:\n")
			fmt.Printf("  Share ID:   %d\n", status.NFSShareID)
			fmt.Printf("  Share Path: %s\n", status.NFSSharePath)
			fmt.Printf("  Enabled:    %t\n", status.NFSEnabled)
		}

		if status.Protocol == tnsapi.ProtocolNVMeOF {
			fmt.Printf("\nNVMe-oF Status:\n")
			fmt.Printf("  Subsystem ID:  %d\n", status.NVMeSubsystemID)
			fmt.Printf("  Namespace ID:  %d\n", status.NVMeNamespaceID)
			fmt.Printf("  NQN:           %s\n", status.NVMeNQN)
		}

		fmt.Printf("\nHealth:     ")
		if status.Healthy {
			fmt.Printf("OK\n")
		} else {
			fmt.Printf("UNHEALTHY\n")
			for _, issue := range status.Issues {
				fmt.Printf("  - %s\n", issue)
			}
		}

		return nil

	default:
		return fmt.Errorf("%w: %s", errStatusUnknownFormat, format)
	}
}
