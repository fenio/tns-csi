package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newListSnapshotsCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) *cobra.Command {
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
			return runListSnapshots(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, clusterID)
		},
	}
	return cmd
}

func runListSnapshots(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) error {
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
	snapshots, err := dashboard.FindManagedSnapshots(ctx, client, *clusterID)
	if err != nil {
		return fmt.Errorf("failed to query snapshots: %w", err)
	}

	// Output based on format
	return outputSnapshots(snapshots, *outputFormat)
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
		t.AppendHeader(table.Row{"NAME", "SOURCE_VOLUME", colProtocol, colType, "SOURCE_DATASET"})
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
