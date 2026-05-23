package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/jedib0t/go-pretty/v6/table"
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

	// datasetTypeVolume is the TrueNAS dataset type for ZVOLs.
	datasetTypeVolume = "VOLUME"
)

func newListCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) *cobra.Command {
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
			return runList(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, clusterID)
		},
	}
	return cmd
}

func runList(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to TrueNAS
	spin := newSpinner("Fetching volumes from TrueNAS...")
	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		spin.stop()
		return err
	}
	defer client.Close()

	// Query all datasets with user properties
	volumes, err := dashboard.FindManagedVolumes(ctx, client, *clusterID)
	spin.stop()
	if err != nil {
		return fmt.Errorf("failed to query volumes: %w", err)
	}

	// Enrich with Kubernetes PV/PVC data (best-effort, no pods for list view)
	k8sData := enrichWithK8sData(ctx, false)
	if k8sData.Available {
		for i := range volumes {
			if binding := dashboard.MatchK8sBinding(k8sData.Bindings, volumes[i].Dataset, volumes[i].VolumeID); binding != nil {
				volumes[i].K8s = binding
			}
		}
	}

	// Output based on format
	return outputVolumes(volumes, *outputFormat)
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
		t := newStyledTable()
		t.AppendHeader(table.Row{colDataset, colVolumeID, colProtocol, "CAPACITY", "PVC", "NAMESPACE", colType, "CLONE_SOURCE", "ADOPTABLE"})
		for i := range volumes {
			v := &volumes[i]
			adoptable := ""
			if v.Adoptable {
				adoptable = colorSuccess.Sprint(valueTrue)
			}
			// Format clone source as "type:id" if present
			cloneSource := ""
			if v.ContentSourceType != "" && v.ContentSourceID != "" {
				cloneSource = fmt.Sprintf("%s:%s", v.ContentSourceType, v.ContentSourceID)
			}
			// K8s PVC/Namespace
			pvcName := colorMuted.Sprint("-")
			pvcNamespace := colorMuted.Sprint("-")
			if v.K8s != nil && v.K8s.PVCName != "" {
				pvcName = v.K8s.PVCName
				pvcNamespace = v.K8s.PVCNamespace
			}
			t.AppendRow(table.Row{v.Dataset, v.VolumeID, protocolBadge(v.Protocol), v.CapacityHuman, pvcName, pvcNamespace, v.Type, cloneSource, adoptable})
		}
		renderTable(t)
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
