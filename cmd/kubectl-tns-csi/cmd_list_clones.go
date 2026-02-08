package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// CloneInfo represents a tns-csi managed cloned volume.
type CloneInfo struct {
	VolumeID       string `json:"volumeId"       yaml:"volumeId"`
	Dataset        string `json:"dataset"        yaml:"dataset"`
	Protocol       string `json:"protocol"       yaml:"protocol"`
	CloneMode      string `json:"cloneMode"      yaml:"cloneMode"`
	SourceType     string `json:"sourceType"     yaml:"sourceType"`
	SourceID       string `json:"sourceId"       yaml:"sourceId"`
	OriginSnapshot string `json:"originSnapshot" yaml:"originSnapshot"`
	DependencyNote string `json:"dependencyNote" yaml:"dependencyNote"`
}

func newListClonesCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-clones",
		Short: "List all tns-csi cloned volumes with dependency info",
		Long: `List all cloned volumes managed by tns-csi on TrueNAS.

Shows clone mode and dependency relationships to help understand
what can and cannot be deleted:

Clone Modes:
  - cow (Copy-on-Write): Clone depends on snapshot. Snapshot CANNOT be deleted.
  - promoted: Snapshot depends on clone. Snapshot CAN be deleted.
  - detached: No dependency. Both can be deleted independently.

Examples:
  # List all clones in table format
  kubectl tns-csi list-clones

  # List all clones in YAML format
  kubectl tns-csi list-clones -o yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListClones(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runListClones(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
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

	// Find all cloned volumes
	clones, err := findClonedVolumes(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to query cloned volumes: %w", err)
	}

	// Output based on format
	return outputClones(clones, *outputFormat)
}

// findClonedVolumes finds all volumes that were cloned from snapshots or other volumes.
func findClonedVolumes(ctx context.Context, client tnsapi.ClientInterface) ([]CloneInfo, error) {
	// Query all managed datasets
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	var clones []CloneInfo
	for _, ds := range datasets {
		// Skip detached snapshots (they're datasets, not volumes)
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		// Check if this volume has a content source (is a clone)
		sourceTypeProp, hasSourceType := ds.UserProperties[tnsapi.PropertyContentSourceType]
		if !hasSourceType || sourceTypeProp.Value == "" {
			continue // Not a clone
		}

		clone := CloneInfo{
			Dataset:    ds.ID,
			SourceType: sourceTypeProp.Value,
		}

		// Extract volume ID
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			clone.VolumeID = prop.Value
		}

		// Extract protocol
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			clone.Protocol = prop.Value
		}

		// Extract source ID
		if prop, ok := ds.UserProperties[tnsapi.PropertyContentSourceID]; ok {
			clone.SourceID = prop.Value
		}

		// Extract clone mode
		if prop, ok := ds.UserProperties[tnsapi.PropertyCloneMode]; ok {
			clone.CloneMode = prop.Value
		} else {
			// Default to COW for legacy clones without mode property
			clone.CloneMode = tnsapi.CloneModeCOW
		}

		// Extract origin snapshot
		if prop, ok := ds.UserProperties[tnsapi.PropertyOriginSnapshot]; ok {
			clone.OriginSnapshot = prop.Value
		}

		// Set dependency note based on mode
		switch clone.CloneMode {
		case tnsapi.CloneModeCOW:
			clone.DependencyNote = "Source snapshot CANNOT be deleted"
		case tnsapi.CloneModePromoted:
			clone.DependencyNote = "Source snapshot CAN be deleted"
		case tnsapi.CloneModeDetached:
			clone.DependencyNote = "Fully independent (no dependencies)"
		default:
			clone.DependencyNote = "Unknown mode"
		}

		clones = append(clones, clone)
	}

	return clones, nil
}

// outputClones outputs clone info in the specified format.
func outputClones(clones []CloneInfo, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(clones)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(clones)

	case outputFormatTable, "":
		if len(clones) == 0 {
			fmt.Println("No cloned volumes found.")
			return nil
		}
		t := newStyledTable()
		t.AppendHeader(table.Row{"VOLUME_ID", "PROTOCOL", "CLONE_MODE", "SOURCE_TYPE", "SOURCE_ID", "DEPENDENCY"})
		for i := range clones {
			var modeStr string
			switch clones[i].CloneMode {
			case tnsapi.CloneModeCOW:
				modeStr = colorError.Sprint("cow")
			case tnsapi.CloneModePromoted:
				modeStr = colorSuccess.Sprint("promoted")
			case tnsapi.CloneModeDetached:
				modeStr = colorProtocolNFS.Sprint("detached")
			default:
				modeStr = clones[i].CloneMode
			}
			t.AppendRow(table.Row{clones[i].VolumeID, protocolBadge(clones[i].Protocol), modeStr, clones[i].SourceType, truncateString(clones[i].SourceID, 30), colorMuted.Sprint(clones[i].DependencyNote)})
		}
		renderTable(t)
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
