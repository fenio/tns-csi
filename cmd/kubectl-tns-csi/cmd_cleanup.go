package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for cleanup command.
var (
	errCleanupAborted       = errors.New("cleanup aborted by user")
	errDatasetNotFoundClean = errors.New("dataset not found for volume")
)

// CleanupResult contains the results of the cleanup operation.
//
//nolint:govet // field alignment not critical for CLI output struct
type CleanupResult struct {
	DryRun  bool                `json:"dryRun"  yaml:"dryRun"`
	Deleted []CleanupVolumeInfo `json:"deleted" yaml:"deleted"`
	Failed  []CleanupVolumeInfo `json:"failed"  yaml:"failed"`
	Skipped []CleanupVolumeInfo `json:"skipped" yaml:"skipped"`
}

// CleanupVolumeInfo contains information about a volume being cleaned up.
type CleanupVolumeInfo struct {
	VolumeID string `json:"volumeId"        yaml:"volumeId"`
	Dataset  string `json:"dataset"         yaml:"dataset"`
	Protocol string `json:"protocol"        yaml:"protocol"`
	Reason   string `json:"reason"          yaml:"reason"`
	Error    string `json:"error,omitempty" yaml:"error,omitempty"`
}

func newCleanupCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		dryRun        bool
		yes           bool
		force         bool
		allNamespaces bool
	)

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete orphaned volumes from TrueNAS",
		Long: `Delete volumes that exist on TrueNAS but have no matching PVC in the cluster.

This command finds orphaned volumes and optionally deletes them from TrueNAS.
For safety, it operates in dry-run mode by default.

Orphaned volumes are those that:
  - Have no corresponding PV in the cluster
  - Have a PV but no bound PVC
  - Were left behind after PVC deletion

Examples:
  # Preview what would be deleted (dry-run, default)
  kubectl tns-csi cleanup

  # Delete orphaned volumes (with confirmation)
  kubectl tns-csi cleanup --execute

  # Delete orphaned volumes without confirmation
  kubectl tns-csi cleanup --execute --yes

  # Force delete volumes not marked as adoptable
  kubectl tns-csi cleanup --execute --force

  # Output in JSON for scripting
  kubectl tns-csi cleanup -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCleanup(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, dryRun, yes, force, allNamespaces)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Preview what would be deleted without making changes")
	cmd.Flags().BoolVar(&dryRun, "execute", false, "Actually delete the volumes (sets dry-run=false)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&force, "force", false, "Delete volumes even if not marked adoptable")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", true, "Search all namespaces for PVCs")

	// Mark execute as inverse of dry-run
	cmd.Flags().Lookup("execute").NoOptDefVal = "true"

	return cmd
}

func runCleanup(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, dryRun, yes, force, allNamespaces bool) error {
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

	// Get Kubernetes client
	k8sClient, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Query all managed volumes from TrueNAS
	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to query volumes: %w", err)
	}

	// Get all PVs and PVCs from Kubernetes
	pvMap, pvcMap, err := getK8sVolumeInfo(ctx, k8sClient, allNamespaces)
	if err != nil {
		return fmt.Errorf("failed to query Kubernetes volumes: %w", err)
	}

	// Find orphaned volumes
	orphaned := findOrphanedVolumes(volumes, pvMap, pvcMap)

	if len(orphaned) == 0 {
		fmt.Println("No orphaned volumes found")
		return nil
	}

	// Build cleanup candidates
	result := &CleanupResult{
		DryRun:  dryRun,
		Deleted: make([]CleanupVolumeInfo, 0),
		Failed:  make([]CleanupVolumeInfo, 0),
		Skipped: make([]CleanupVolumeInfo, 0),
	}

	// Filter and categorize volumes
	var toDelete []OrphanedVolumeInfo
	for i := range orphaned {
		vol := &orphaned[i]
		if !vol.Adoptable && !force {
			result.Skipped = append(result.Skipped, CleanupVolumeInfo{
				VolumeID: vol.VolumeID,
				Dataset:  vol.Dataset,
				Protocol: vol.Protocol,
				Reason:   "not marked adoptable (use --force to override)",
			})
			continue
		}
		toDelete = append(toDelete, *vol)
	}

	if len(toDelete) == 0 {
		if len(result.Skipped) > 0 {
			fmt.Printf("Found %d orphaned volume(s), but all were skipped (not adoptable)\n", len(result.Skipped))
			fmt.Println("Use --force to delete volumes not marked as adoptable")
		}
		return outputCleanupResult(result, *outputFormat)
	}

	// Show what will be deleted
	if dryRun || !yes {
		fmt.Printf("Found %d orphaned volume(s) to delete:\n\n", len(toDelete))
		showCleanupPreview(toDelete)
		fmt.Println()
	}

	// If dry-run, just show preview
	if dryRun {
		fmt.Println("Dry-run mode: No changes made. Use --execute to actually delete volumes.")
		for i := range toDelete {
			vol := &toDelete[i]
			result.Deleted = append(result.Deleted, CleanupVolumeInfo{
				VolumeID: vol.VolumeID,
				Dataset:  vol.Dataset,
				Protocol: vol.Protocol,
				Reason:   vol.Reason,
			})
		}
		return outputCleanupResult(result, *outputFormat)
	}

	// Confirm deletion
	if !yes {
		fmt.Print("Are you sure you want to delete these volumes? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			return errCleanupAborted
		}
		fmt.Println()
	}

	// Delete volumes
	for i := range toDelete {
		vol := &toDelete[i]
		info := CleanupVolumeInfo{
			VolumeID: vol.VolumeID,
			Dataset:  vol.Dataset,
			Protocol: vol.Protocol,
			Reason:   vol.Reason,
		}

		fmt.Printf("Deleting %s (%s)... ", vol.VolumeID, vol.Protocol)

		err := deleteOrphanedVolume(ctx, client, vol)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			info.Error = err.Error()
			result.Failed = append(result.Failed, info)
		} else {
			fmt.Println("OK")
			result.Deleted = append(result.Deleted, info)
		}
	}

	fmt.Println()
	fmt.Printf("Deleted: %d, Failed: %d, Skipped: %d\n",
		len(result.Deleted), len(result.Failed), len(result.Skipped))

	return outputCleanupResult(result, *outputFormat)
}

// deleteOrphanedVolume deletes a volume and its associated resources from TrueNAS.
func deleteOrphanedVolume(ctx context.Context, client tnsapi.ClientInterface, vol *OrphanedVolumeInfo) error {
	// Get the dataset with full properties to find resource IDs
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyCSIVolumeName, vol.VolumeID)
	if err != nil {
		return fmt.Errorf("failed to find dataset: %w", err)
	}

	if len(datasets) == 0 {
		return fmt.Errorf("%w: %s", errDatasetNotFoundClean, vol.VolumeID)
	}

	ds := &datasets[0]

	switch vol.Protocol {
	case protocolNFS:
		return deleteNFSVolumeResources(ctx, client, ds)
	case protocolNVMeOF:
		return deleteNVMeOFVolumeResources(ctx, client, ds)
	default:
		// Unknown protocol - just try to delete the dataset
		return client.DeleteDataset(ctx, ds.ID)
	}
}

// deleteNFSVolumeResources deletes NFS share and dataset.
func deleteNFSVolumeResources(ctx context.Context, client tnsapi.ClientInterface, ds *tnsapi.DatasetWithProperties) error {
	// Get NFS share ID from properties
	if prop, ok := ds.UserProperties[tnsapi.PropertyNFSShareID]; ok && prop.Value != "" {
		shareID, err := strconv.Atoi(prop.Value)
		if err == nil && shareID > 0 {
			// Delete NFS share first
			if err := client.DeleteNFSShare(ctx, shareID); err != nil {
				// Log but continue - share may already be deleted
				fmt.Printf("(warning: failed to delete NFS share %d: %v) ", shareID, err)
			}
		}
	}

	// Delete the dataset
	return client.DeleteDataset(ctx, ds.ID)
}

// deleteNVMeOFVolumeResources deletes NVMe-oF subsystem, namespace, and zvol.
func deleteNVMeOFVolumeResources(ctx context.Context, client tnsapi.ClientInterface, ds *tnsapi.DatasetWithProperties) error {
	// Get namespace ID and delete it first
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeNamespaceID]; ok && prop.Value != "" {
		nsID, err := strconv.Atoi(prop.Value)
		if err == nil && nsID > 0 {
			if err := client.DeleteNVMeOFNamespace(ctx, nsID); err != nil {
				// Log but continue
				fmt.Printf("(warning: failed to delete NVMe namespace %d: %v) ", nsID, err)
			}
		}
	}

	// Get subsystem ID and delete it
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeSubsystemID]; ok && prop.Value != "" {
		subsysID, err := strconv.Atoi(prop.Value)
		if err == nil && subsysID > 0 {
			if err := client.DeleteNVMeOFSubsystem(ctx, subsysID); err != nil {
				// Log but continue
				fmt.Printf("(warning: failed to delete NVMe subsystem %d: %v) ", subsysID, err)
			}
		}
	}

	// Delete the zvol
	return client.DeleteDataset(ctx, ds.ID)
}

// showCleanupPreview displays the volumes that will be deleted.
//
//nolint:errcheck // writing to tabwriter for stdout - errors not actionable
func showCleanupPreview(volumes []OrphanedVolumeInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "VOLUME_ID\tPROTOCOL\tDATASET\tREASON")
	for i := range volumes {
		v := &volumes[i]
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.VolumeID, v.Protocol, v.Dataset, v.Reason)
	}
	_ = w.Flush()
}

// outputCleanupResult outputs the cleanup result in the specified format.
func outputCleanupResult(result *CleanupResult, format string) error {
	// For table format, we've already printed progress
	if format == outputFormatTable || format == "" {
		return nil
	}

	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(result)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
