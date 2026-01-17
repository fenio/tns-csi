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

// Static errors for mark-adoptable command.
var errNoVolumesSpecified = errors.New("no volumes specified")

// MarkAdoptableResult contains the results of the mark-adoptable operation.
type MarkAdoptableResult struct {
	Action    string                    `json:"action"    yaml:"action"`
	Succeeded []MarkAdoptableVolumeInfo `json:"succeeded" yaml:"succeeded"`
	Failed    []MarkAdoptableVolumeInfo `json:"failed"    yaml:"failed"`
}

// MarkAdoptableVolumeInfo contains information about a volume being marked.
type MarkAdoptableVolumeInfo struct {
	VolumeID string `json:"volumeId"        yaml:"volumeId"`
	Dataset  string `json:"dataset"         yaml:"dataset"`
	Error    string `json:"error,omitempty" yaml:"error,omitempty"`
}

func newMarkAdoptableCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		unmark bool
		all    bool
	)

	cmd := &cobra.Command{
		Use:   "mark-adoptable [volume-id...]",
		Short: "Mark volumes as adoptable for disaster recovery",
		Long: `Mark volumes as adoptable so they can be adopted into a new cluster.

Adoptable volumes can be:
  - Deleted by the cleanup command
  - Adopted into a new cluster using the adopt command
  - Identified as safe to reclaim during disaster recovery

Use --unmark to remove the adoptable flag from volumes.

Examples:
  # Mark a single volume as adoptable
  kubectl tns-csi mark-adoptable pvc-12345678-1234-1234-1234-123456789012

  # Mark multiple volumes as adoptable
  kubectl tns-csi mark-adoptable pvc-xxx pvc-yyy pvc-zzz

  # Mark all volumes as adoptable (for DR preparation)
  kubectl tns-csi mark-adoptable --all

  # Remove adoptable flag from a volume
  kubectl tns-csi mark-adoptable --unmark pvc-xxx

  # Remove adoptable flag from all volumes
  kubectl tns-csi mark-adoptable --unmark --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMarkAdoptable(cmd.Context(), args, url, apiKey, secretRef, outputFormat, skipTLSVerify, unmark, all)
		},
	}

	cmd.Flags().BoolVar(&unmark, "unmark", false, "Remove the adoptable flag instead of setting it")
	cmd.Flags().BoolVar(&all, "all", false, "Mark/unmark all managed volumes")

	return cmd
}

func runMarkAdoptable(ctx context.Context, args []string, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, unmark, all bool) error {
	// Validate args
	if !all && len(args) == 0 {
		return errNoVolumesSpecified
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

	// Get volumes to mark
	var volumes []VolumeInfo
	if all {
		volumes, err = findManagedVolumes(ctx, client)
		if err != nil {
			return fmt.Errorf("failed to query volumes: %w", err)
		}
	} else {
		// Find each specified volume
		for _, volumeRef := range args {
			vol, err := findVolumeByRef(ctx, client, volumeRef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: volume not found: %s\n", volumeRef)
				continue
			}
			volumes = append(volumes, *vol)
		}
	}

	if len(volumes) == 0 {
		fmt.Println("No volumes to process")
		return nil
	}

	// Mark/unmark volumes
	result := &MarkAdoptableResult{
		Action:    "mark",
		Succeeded: make([]MarkAdoptableVolumeInfo, 0),
		Failed:    make([]MarkAdoptableVolumeInfo, 0),
	}
	if unmark {
		result.Action = "unmark"
	}

	for i := range volumes {
		vol := &volumes[i]
		info := MarkAdoptableVolumeInfo{
			VolumeID: vol.VolumeID,
			Dataset:  vol.Dataset,
		}

		var err error
		if unmark {
			err = clearAdoptableFlag(ctx, client, vol.Dataset)
		} else {
			err = setAdoptableFlag(ctx, client, vol.Dataset)
		}

		if err != nil {
			info.Error = err.Error()
			result.Failed = append(result.Failed, info)
			if *outputFormat == outputFormatTable || *outputFormat == "" {
				fmt.Printf("%s %s: FAILED (%v)\n", actionVerb(unmark), vol.VolumeID, err)
			}
		} else {
			result.Succeeded = append(result.Succeeded, info)
			if *outputFormat == outputFormatTable || *outputFormat == "" {
				fmt.Printf("%s %s: OK\n", actionVerb(unmark), vol.VolumeID)
			}
		}
	}

	// Summary for table format
	if *outputFormat == outputFormatTable || *outputFormat == "" {
		fmt.Println()
		fmt.Printf("Succeeded: %d, Failed: %d\n", len(result.Succeeded), len(result.Failed))
	}

	return outputMarkAdoptableResult(result, *outputFormat)
}

// findVolumeByRef finds a volume by volume ID or dataset path.
func findVolumeByRef(ctx context.Context, client tnsapi.ClientInterface, volumeRef string) (*VolumeInfo, error) {
	// Try to find by CSI volume name first
	ds, err := client.FindDatasetByCSIVolumeName(ctx, "", volumeRef)
	if err == nil && ds != nil {
		return datasetToVolumeInfo(ds), nil
	}

	// Try to find by dataset path
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	for i := range datasets {
		if datasets[i].ID == volumeRef {
			return datasetToVolumeInfo(&datasets[i]), nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errVolumeNotFound, volumeRef)
}

// datasetToVolumeInfo converts a dataset to VolumeInfo.
func datasetToVolumeInfo(ds *tnsapi.DatasetWithProperties) *VolumeInfo {
	info := &VolumeInfo{
		Dataset: ds.ID,
	}

	if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
		info.VolumeID = prop.Value
	}
	if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
		info.Protocol = prop.Value
	}
	if prop, ok := ds.UserProperties[tnsapi.PropertyAdoptable]; ok {
		info.Adoptable = prop.Value == valueTrue
	}

	return info
}

// setAdoptableFlag sets the adoptable flag on a dataset.
func setAdoptableFlag(ctx context.Context, client tnsapi.ClientInterface, datasetID string) error {
	props := map[string]string{
		tnsapi.PropertyAdoptable: tnsapi.PropertyValueTrue,
	}
	return client.SetDatasetProperties(ctx, datasetID, props)
}

// clearAdoptableFlag removes the adoptable flag from a dataset.
func clearAdoptableFlag(ctx context.Context, client tnsapi.ClientInterface, datasetID string) error {
	return client.ClearDatasetProperties(ctx, datasetID, []string{tnsapi.PropertyAdoptable})
}

// actionVerb returns the appropriate verb for the action.
func actionVerb(unmark bool) string {
	if unmark {
		return "Unmarking"
	}
	return "Marking"
}

// outputMarkAdoptableResult outputs the result in the specified format.
func outputMarkAdoptableResult(result *MarkAdoptableResult, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(result)

	case outputFormatTable, "":
		// Already printed progress above
		if len(result.Failed) > 0 {
			fmt.Println("\nFailed volumes:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			//nolint:errcheck // writing to tabwriter for stdout
			_, _ = fmt.Fprintln(w, "VOLUME_ID\tERROR")
			for i := range result.Failed {
				v := &result.Failed[i]
				//nolint:errcheck // writing to tabwriter for stdout
				_, _ = fmt.Fprintf(w, "%s\t%s\n", v.VolumeID, v.Error)
			}
			//nolint:errcheck // flushing tabwriter for stdout
			_ = w.Flush()
		}
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
