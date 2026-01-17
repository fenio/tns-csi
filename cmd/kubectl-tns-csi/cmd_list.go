package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for list command.
var errUnknownOutputFormat = errors.New("unknown output format")

// Output format constants.
const (
	outputFormatJSON  = "json"
	outputFormatYAML  = "yaml"
	outputFormatTable = "table"
	valueTrue         = "true"
)

// VolumeInfo represents a tns-csi managed volume.
type VolumeInfo struct {
	Dataset        string `json:"dataset"        yaml:"dataset"`
	VolumeID       string `json:"volumeId"       yaml:"volumeId"`
	Protocol       string `json:"protocol"       yaml:"protocol"`
	CapacityHuman  string `json:"capacityHuman"  yaml:"capacityHuman"`
	DeleteStrategy string `json:"deleteStrategy" yaml:"deleteStrategy"`
	Type           string `json:"type"           yaml:"type"`
	CapacityBytes  int64  `json:"capacityBytes"  yaml:"capacityBytes"`
	Adoptable      bool   `json:"adoptable"      yaml:"adoptable"`
}

func newListCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all tns-csi managed volumes on TrueNAS",
		Long: `List all volumes managed by tns-csi on TrueNAS.

This command queries TrueNAS for all datasets with tns-csi:managed_by property
and displays their metadata.

Examples:
  # List all volumes in table format
  kubectl tns-csi list

  # List all volumes in YAML format
  kubectl tns-csi list -o yaml

  # List volumes using specific TrueNAS connection
  kubectl tns-csi list --url wss://truenas:443/api/current --api-key <key>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runList(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
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

	// Query all datasets with user properties
	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to query volumes: %w", err)
	}

	// Output based on format
	return outputVolumes(volumes, *outputFormat)
}

// findManagedVolumes finds all datasets managed by tns-csi.
func findManagedVolumes(ctx context.Context, client tnsapi.ClientInterface) ([]VolumeInfo, error) {
	// Query all datasets with user properties
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	var volumes []VolumeInfo
	for _, ds := range datasets {
		// Skip detached snapshots (they're datasets but not volumes)
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		// Skip datasets without volume ID (parent datasets, etc.)
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID == "" {
			continue
		}

		vol := VolumeInfo{
			Dataset:  ds.ID,
			VolumeID: volumeID,
			Type:     ds.Type,
		}

		// Extract protocol
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			vol.Protocol = prop.Value
		}

		// Extract capacity
		if prop, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
			vol.CapacityBytes = tnsapi.StringToInt64(prop.Value)
			vol.CapacityHuman = formatBytes(vol.CapacityBytes)
		}

		// Extract delete strategy
		if prop, ok := ds.UserProperties[tnsapi.PropertyDeleteStrategy]; ok {
			vol.DeleteStrategy = prop.Value
		}

		// Extract adoptable flag
		if prop, ok := ds.UserProperties[tnsapi.PropertyAdoptable]; ok {
			vol.Adoptable = prop.Value == valueTrue
		}

		volumes = append(volumes, vol)
	}

	return volumes, nil
}

// outputVolumes outputs volumes in the specified format.
func outputVolumes(volumes []VolumeInfo, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(volumes)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(volumes)

	case outputFormatTable, "":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		//nolint:errcheck // writing to tabwriter for stdout
		_, _ = fmt.Fprintln(w, "DATASET\tVOLUME_ID\tPROTOCOL\tCAPACITY\tTYPE\tADOPTABLE")
		for _, v := range volumes {
			adoptable := ""
			if v.Adoptable {
				adoptable = valueTrue
			}
			//nolint:errcheck // writing to tabwriter for stdout
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				v.Dataset, v.VolumeID, v.Protocol, v.CapacityHuman, v.Type, adoptable)
		}
		return w.Flush()

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// formatBytes converts bytes to human-readable format.
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1fTi", float64(bytes)/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.1fGi", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1fMi", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1fKi", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
