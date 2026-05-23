package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newListUnmanagedCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) *cobra.Command {
	var (
		pool       string
		parentPath string
		showAll    bool
	)

	cmd := &cobra.Command{
		Use:   "list-unmanaged",
		Short: "List volumes not managed by tns-csi",
		Long: `List all datasets and zvols on TrueNAS that are not managed by tns-csi.

This command helps identify volumes that could be imported into tns-csi management,
such as volumes created by democratic-csi, manual creation, or other tools.

The command shows:
  - Dataset path and name
  - Type (filesystem or zvol)
  - Detected protocol (NFS if share exists, NVMe-oF for zvols)
  - Size information
  - Any existing management markers (e.g., democratic-csi)

Examples:
  # List unmanaged volumes in a specific pool
  kubectl tns-csi list-unmanaged --pool storage

  # List unmanaged volumes under a specific parent dataset
  kubectl tns-csi list-unmanaged --parent storage/k8s

  # Show all datasets including system datasets
  kubectl tns-csi list-unmanaged --pool storage --all

  # Output as JSON for scripting
  kubectl tns-csi list-unmanaged --pool storage -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListUnmanaged(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, clusterID,
				pool, parentPath, showAll)
		},
	}

	cmd.Flags().StringVar(&pool, "pool", "", "ZFS pool to search in (required if --parent not specified)")
	cmd.Flags().StringVar(&parentPath, "parent", "", "Parent dataset path to search under")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show all datasets including system datasets")

	return cmd
}

func runListUnmanaged(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string,
	pool, parentPath string, showAll bool) error {

	if pool == "" && parentPath == "" {
		return errPoolOrParentMissing
	}

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

	// Determine search path
	searchPath := parentPath
	if searchPath == "" {
		searchPath = pool
	}

	// Find unmanaged volumes
	volumes, err := dashboard.FindUnmanagedVolumes(ctx, client, searchPath, showAll, *clusterID)
	if err != nil {
		return fmt.Errorf("failed to find unmanaged volumes: %w", err)
	}

	if len(volumes) == 0 {
		fmt.Println("No unmanaged volumes found")
		return nil
	}

	return outputUnmanagedVolumes(volumes, *outputFormat)
}

func outputUnmanagedVolumes(volumes []UnmanagedVolume, format string) error {
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
		t := newStyledTable()
		t.AppendHeader(table.Row{colDataset, colType, colProtocol, "SIZE", "MANAGED_BY"})

		for i := range volumes {
			v := &volumes[i]
			managedBy := colorMuted.Sprint("-")
			if v.ManagedBy != "" {
				managedBy = colorWarning.Sprint(v.ManagedBy)
			}
			t.AppendRow(table.Row{v.Dataset, strings.ToLower(v.Type), protocolBadge(v.Protocol), v.Size, managedBy})
		}

		renderTable(t)

		fmt.Printf("\nFound %d unmanaged volume(s)\n", len(volumes))
		fmt.Println("Use 'kubectl tns-csi import <dataset>' to import a volume into tns-csi management")
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
