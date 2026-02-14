package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// SnapshotInfo represents a tns-csi managed snapshot.
type SnapshotInfo struct {
	Name           string `json:"name"           yaml:"name"`
	SourceVolume   string `json:"sourceVolume"   yaml:"sourceVolume"`
	SourceDataset  string `json:"sourceDataset"  yaml:"sourceDataset"`
	Protocol       string `json:"protocol"       yaml:"protocol"`
	Type           string `json:"type"           yaml:"type"` // "attached" or "detached"
	DeleteStrategy string `json:"deleteStrategy" yaml:"deleteStrategy"`
}

func newListSnapshotsCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-snapshots",
		Short: "List all tns-csi managed snapshots on TrueNAS",
		Long: `List all snapshots managed by tns-csi on TrueNAS.

This command queries TrueNAS for all snapshots associated with tns-csi managed
volumes, including both attached (on-volume) and detached snapshots.

Examples:
  # List all snapshots in table format
  kubectl tns-csi list-snapshots

  # List all snapshots in YAML format
  kubectl tns-csi list-snapshots -o yaml

  # List snapshots using specific TrueNAS connection
  kubectl tns-csi list-snapshots --url wss://truenas:443/api/current --api-key <key>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListSnapshots(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runListSnapshots(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
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

	// Find all snapshots
	snapshots, err := findManagedSnapshots(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to query snapshots: %w", err)
	}

	// Output based on format
	return outputSnapshots(snapshots, *outputFormat)
}

// findManagedSnapshots finds all snapshots managed by tns-csi.
func findManagedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	var snapshots []SnapshotInfo

	// 1. Find attached snapshots (ZFS snapshots on managed datasets)
	attachedSnapshots, err := findAttachedSnapshots(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to find attached snapshots: %w", err)
	}
	snapshots = append(snapshots, attachedSnapshots...)

	// 2. Find detached snapshots (datasets with detached_snapshot=true)
	detachedSnapshots, err := findDetachedSnapshots(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to find detached snapshots: %w", err)
	}
	snapshots = append(snapshots, detachedSnapshots...)

	return snapshots, nil
}

// findAttachedSnapshots finds ZFS snapshots on managed datasets.
func findAttachedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	// First get all managed datasets to know which snapshots to look for
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	// Build a map of managed dataset IDs to their metadata
	managedDatasets := make(map[string]struct {
		volumeID string
		protocol string
	})
	for _, ds := range datasets {
		// Skip detached snapshots (they're datasets)
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		protocol := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			protocol = prop.Value
		}
		if volumeID != "" {
			managedDatasets[ds.ID] = struct {
				volumeID string
				protocol string
			}{volumeID: volumeID, protocol: protocol}
		}
	}

	// Query snapshots per managed dataset (avoids fetching all snapshots globally,
	// which can cause buffer overflow on systems with many non-CSI datasets)
	var snapshots []SnapshotInfo
	for datasetID, meta := range managedDatasets {
		datasetSnaps, queryErr := client.QuerySnapshots(ctx, []interface{}{
			[]interface{}{"dataset", "=", datasetID},
		})
		if queryErr != nil {
			return nil, fmt.Errorf("failed to query snapshots for dataset %s: %w", datasetID, queryErr)
		}

		for _, snap := range datasetSnaps {
			snapshots = append(snapshots, SnapshotInfo{
				Name:          snap.Name,
				SourceVolume:  meta.volumeID,
				SourceDataset: snap.Dataset,
				Protocol:      meta.protocol,
				Type:          "attached",
			})
		}
	}

	return snapshots, nil
}

// findDetachedSnapshots finds detached snapshot datasets.
func findDetachedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	// Query datasets with detached_snapshot=true
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyDetachedSnapshot, valueTrue)
	if err != nil {
		return nil, err
	}

	var snapshots []SnapshotInfo
	for _, ds := range datasets {
		// Verify it's managed by tns-csi
		if prop, ok := ds.UserProperties[tnsapi.PropertyManagedBy]; !ok || prop.Value != tnsapi.ManagedByValue {
			continue
		}

		snap := SnapshotInfo{
			Type: "detached",
		}

		// Extract snapshot ID (name)
		if prop, ok := ds.UserProperties[tnsapi.PropertySnapshotID]; ok {
			snap.Name = prop.Value
		} else {
			// Use dataset name as fallback
			parts := strings.Split(ds.ID, "/")
			snap.Name = parts[len(parts)-1]
		}

		// Extract source volume
		if prop, ok := ds.UserProperties[tnsapi.PropertySourceVolumeID]; ok {
			snap.SourceVolume = prop.Value
		}

		// Extract source dataset
		if prop, ok := ds.UserProperties[tnsapi.PropertySourceDataset]; ok {
			snap.SourceDataset = prop.Value
		}

		// Extract protocol
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			snap.Protocol = prop.Value
		}

		// Extract delete strategy
		if prop, ok := ds.UserProperties[tnsapi.PropertyDeleteStrategy]; ok {
			snap.DeleteStrategy = prop.Value
		}

		snapshots = append(snapshots, snap)
	}

	return snapshots, nil
}

// outputSnapshots outputs snapshots in the specified format.
func outputSnapshots(snapshots []SnapshotInfo, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshots)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(snapshots)

	case outputFormatTable, "":
		t := newStyledTable()
		t.AppendHeader(table.Row{"NAME", "SOURCE_VOLUME", "PROTOCOL", "TYPE", "SOURCE_DATASET"})
		for _, s := range snapshots {
			snapType := colorSuccess.Sprint(s.Type)
			if s.Type == "detached" {
				snapType = colorProtocolNFS.Sprint(s.Type)
			}
			t.AppendRow(table.Row{s.Name, s.SourceVolume, protocolBadge(s.Protocol), snapType, s.SourceDataset})
		}
		renderTable(t)
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
