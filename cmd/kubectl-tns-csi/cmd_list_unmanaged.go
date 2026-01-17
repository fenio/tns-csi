package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// UnmanagedVolume represents a volume not managed by tns-csi.
//
//nolint:govet // field alignment not critical for CLI output struct
type UnmanagedVolume struct {
	Dataset      string `json:"dataset"                yaml:"dataset"`
	Name         string `json:"name"                   yaml:"name"`
	Type         string `json:"type"                   yaml:"type"` // "filesystem" or "volume" (zvol)
	Protocol     string `json:"protocol"               yaml:"protocol"`
	Size         string `json:"size"                   yaml:"size"`
	SizeBytes    int64  `json:"sizeBytes"              yaml:"sizeBytes"`
	NFSShareID   int    `json:"nfsShareId,omitempty"   yaml:"nfsShareId,omitempty"`
	NFSSharePath string `json:"nfsSharePath,omitempty" yaml:"nfsSharePath,omitempty"`
	ManagedBy    string `json:"managedBy,omitempty"    yaml:"managedBy,omitempty"` // e.g., "democratic-csi"
}

func newListUnmanagedCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
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
			return runListUnmanaged(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify,
				pool, parentPath, showAll)
		},
	}

	cmd.Flags().StringVar(&pool, "pool", "", "ZFS pool to search in (required if --parent not specified)")
	cmd.Flags().StringVar(&parentPath, "parent", "", "Parent dataset path to search under")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show all datasets including system datasets")

	return cmd
}

func runListUnmanaged(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool,
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
	volumes, err := findUnmanagedVolumes(ctx, client, searchPath, showAll)
	if err != nil {
		return fmt.Errorf("failed to find unmanaged volumes: %w", err)
	}

	if len(volumes) == 0 {
		fmt.Println("No unmanaged volumes found")
		return nil
	}

	return outputUnmanagedVolumes(volumes, *outputFormat)
}

func findUnmanagedVolumes(ctx context.Context, client tnsapi.ClientInterface, searchPath string, showAll bool) ([]UnmanagedVolume, error) {
	// Get all datasets under the search path
	allDatasets, err := client.QueryAllDatasets(ctx, searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	// Get managed datasets to filter them out
	managedDatasets, err := client.FindManagedDatasets(ctx, searchPath)
	if err != nil {
		// Non-fatal - maybe none are managed
		managedDatasets = nil
	}

	// Build lookup of managed dataset IDs
	managedIDs := make(map[string]bool)
	for i := range managedDatasets {
		managedIDs[managedDatasets[i].ID] = true
	}

	// Get all NFS shares for matching
	nfsShares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		// Non-fatal, continue without NFS info
		nfsShares = nil
	}

	// Build NFS share lookup by path
	nfsShareByPath := make(map[string]*tnsapi.NFSShare)
	for i := range nfsShares {
		nfsShareByPath[nfsShares[i].Path] = &nfsShares[i]
	}

	// Try to detect democratic-csi managed datasets
	//nolint:errcheck // non-fatal if this fails
	democraticDatasets, _ := client.FindDatasetsByProperty(ctx, searchPath, "org.democratic-csi:managed_by", "")

	democraticIDs := make(map[string]string)
	for i := range democraticDatasets {
		democraticIDs[democraticDatasets[i].ID] = "democratic-csi"
	}

	var volumes []UnmanagedVolume

	for i := range allDatasets {
		ds := &allDatasets[i]

		// Skip the root dataset itself
		if ds.ID == searchPath {
			continue
		}

		// Skip if managed by tns-csi
		if managedIDs[ds.ID] {
			continue
		}

		// Skip system datasets unless --all is specified
		if !showAll && isSystemDataset(ds.ID, searchPath) {
			continue
		}

		vol := UnmanagedVolume{
			Dataset: ds.ID,
			Name:    extractDatasetName(ds.ID),
			Type:    ds.Type,
		}

		// Get size
		if ds.Used != nil {
			if val, ok := ds.Used["parsed"].(float64); ok {
				vol.SizeBytes = int64(val)
				vol.Size = formatBytes(vol.SizeBytes)
			}
		}

		// Check for NFS share
		if share, ok := nfsShareByPath[ds.Mountpoint]; ok {
			vol.Protocol = protocolNFS
			vol.NFSShareID = share.ID
			vol.NFSSharePath = share.Path
		} else if ds.Type == "VOLUME" {
			// ZVOLs are typically used for block storage
			vol.Protocol = protocolNVMeOF
		} else {
			vol.Protocol = "unknown"
		}

		// Check for democratic-csi management
		if manager, ok := democraticIDs[ds.ID]; ok {
			vol.ManagedBy = manager
		}

		volumes = append(volumes, vol)
	}

	return volumes, nil
}

func isSystemDataset(datasetID, searchPath string) bool {
	// Get the relative path
	relPath := strings.TrimPrefix(datasetID, searchPath+"/")

	// Skip common system/infrastructure datasets
	systemPrefixes := []string{
		"ix-applications",
		"ix-",
		".system",
		"iocage",
	}

	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(relPath, prefix) {
			return true
		}
	}

	return false
}

func extractDatasetName(datasetID string) string {
	parts := strings.Split(datasetID, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return datasetID
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
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		//nolint:errcheck // writing to tabwriter for stdout
		_, _ = fmt.Fprintln(w, "DATASET\tTYPE\tPROTOCOL\tSIZE\tMANAGED_BY")

		for i := range volumes {
			v := &volumes[i]
			managedBy := v.ManagedBy
			if managedBy == "" {
				managedBy = "-"
			}
			//nolint:errcheck // writing to tabwriter for stdout
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				v.Dataset,
				strings.ToLower(v.Type),
				v.Protocol,
				v.Size,
				managedBy,
			)
		}

		//nolint:errcheck // flushing tabwriter for stdout
		_ = w.Flush()

		fmt.Printf("\nFound %d unmanaged volume(s)\n", len(volumes))
		fmt.Println("Use 'kubectl tns-csi import <dataset>' to import a volume into tns-csi management")
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
