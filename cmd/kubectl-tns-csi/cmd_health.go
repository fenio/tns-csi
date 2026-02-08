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

// HealthStatus represents the health status of a volume.
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "Healthy"
	HealthStatusDegraded  HealthStatus = "Degraded"
	HealthStatusUnhealthy HealthStatus = "Unhealthy"
)

// VolumeHealth represents the health status of a single volume.
//
//nolint:govet // field alignment not critical for CLI output struct
type VolumeHealth struct {
	VolumeID  string       `json:"volumeId"           yaml:"volumeId"`
	Dataset   string       `json:"dataset"            yaml:"dataset"`
	Protocol  string       `json:"protocol"           yaml:"protocol"`
	Status    HealthStatus `json:"status"             yaml:"status"`
	Issues    []string     `json:"issues"             yaml:"issues"`
	ShareOK   *bool        `json:"shareOk,omitempty"  yaml:"shareOk,omitempty"`  // NFS: share exists and enabled
	SubsysOK  *bool        `json:"subsysOk,omitempty" yaml:"subsysOk,omitempty"` // NVMe-oF: subsystem exists and enabled
	DatasetOK bool         `json:"datasetOk"          yaml:"datasetOk"`          // Dataset exists
}

// HealthReport contains the overall health report.
//
//nolint:govet // field alignment not critical for CLI output struct
type HealthReport struct {
	Summary  HealthSummary  `json:"summary"  yaml:"summary"`
	Volumes  []VolumeHealth `json:"volumes"  yaml:"volumes"`
	Problems []VolumeHealth `json:"problems" yaml:"problems"` // Only volumes with issues
}

// HealthSummary contains summary statistics.
type HealthSummary struct {
	TotalVolumes     int `json:"totalVolumes"     yaml:"totalVolumes"`
	HealthyVolumes   int `json:"healthyVolumes"   yaml:"healthyVolumes"`
	DegradedVolumes  int `json:"degradedVolumes"  yaml:"degradedVolumes"`
	UnhealthyVolumes int `json:"unhealthyVolumes" yaml:"unhealthyVolumes"`
}

func newHealthCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check health of all tns-csi managed volumes",
		Long: `Check the health status of all tns-csi managed volumes on TrueNAS.

This command verifies:
  - Dataset exists on TrueNAS
  - NFS shares are present and enabled (for NFS volumes)
  - NVMe-oF subsystems are present and enabled (for NVMe-oF volumes)

By default, only volumes with issues are shown. Use --all to show all volumes.

Examples:
  # Show only volumes with issues
  kubectl tns-csi health

  # Show all volumes including healthy ones
  kubectl tns-csi health --all

  # Output as JSON
  kubectl tns-csi health -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHealth(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, showAll)
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all volumes, not just those with issues")
	return cmd
}

func runHealth(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, showAll bool) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to TrueNAS
	spin := newSpinner("Checking volume health...")
	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		spin.stop()
		return err
	}
	defer client.Close()

	// Check health of all volumes
	report, err := checkVolumeHealth(ctx, client)
	spin.stop()
	if err != nil {
		return fmt.Errorf("failed to check health: %w", err)
	}

	// Output based on format
	return outputHealthReport(report, *outputFormat, showAll)
}

// checkVolumeHealth checks the health of all managed volumes.
func checkVolumeHealth(ctx context.Context, client tnsapi.ClientInterface) (*HealthReport, error) {
	// Get all managed volumes
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	// Get all NFS shares for quick lookup
	nfsShares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		// Non-fatal, we'll just mark NFS checks as unknown
		nfsShares = nil
	}
	nfsShareMap := make(map[string]*tnsapi.NFSShare)
	for i := range nfsShares {
		nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}

	// Get all NVMe-oF subsystems for quick lookup
	nvmeSubsystems, err := client.ListAllNVMeOFSubsystems(ctx)
	if err != nil {
		// Non-fatal
		nvmeSubsystems = nil
	}
	nvmeSubsysMap := make(map[string]*tnsapi.NVMeOFSubsystem)
	for i := range nvmeSubsystems {
		// Map by both name and NQN
		nvmeSubsysMap[nvmeSubsystems[i].Name] = &nvmeSubsystems[i]
		nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}

	report := &HealthReport{
		Volumes:  make([]VolumeHealth, 0),
		Problems: make([]VolumeHealth, 0),
	}

	for i := range datasets {
		ds := &datasets[i]

		// Skip detached snapshots
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		// Skip datasets without volume ID
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID == "" {
			continue
		}

		health := VolumeHealth{
			VolumeID:  volumeID,
			Dataset:   ds.ID,
			DatasetOK: true, // We found the dataset, so it exists
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		// Get protocol
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			health.Protocol = prop.Value
		}

		// Check protocol-specific health
		switch health.Protocol {
		case protocolNFS:
			checkNFSHealth(ds, nfsShareMap, &health)
		case protocolNVMeOF:
			checkNVMeOFHealth(ds, nvmeSubsysMap, &health)
		}

		// Determine overall status
		if len(health.Issues) > 0 {
			health.Status = HealthStatusDegraded
			// Check for critical issues
			for _, issue := range health.Issues {
				issueLower := strings.ToLower(issue)
				if strings.Contains(issueLower, "not found") || strings.Contains(issueLower, "disabled") {
					health.Status = HealthStatusUnhealthy
					break
				}
			}
		}

		// Update summary
		report.Summary.TotalVolumes++
		switch health.Status {
		case HealthStatusHealthy:
			report.Summary.HealthyVolumes++
		case HealthStatusDegraded:
			report.Summary.DegradedVolumes++
		case HealthStatusUnhealthy:
			report.Summary.UnhealthyVolumes++
		}

		report.Volumes = append(report.Volumes, health)
		if health.Status != HealthStatusHealthy {
			report.Problems = append(report.Problems, health)
		}
	}

	return report, nil
}

// checkNFSHealth checks NFS-specific health for a volume.
func checkNFSHealth(ds *tnsapi.DatasetWithProperties, nfsShareMap map[string]*tnsapi.NFSShare, health *VolumeHealth) {
	// Get expected share path
	sharePath := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
	}

	if sharePath == "" {
		health.Issues = append(health.Issues, "NFS share path not found in properties")
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	// Check if share exists
	share, exists := nfsShareMap[sharePath]
	if !exists {
		health.Issues = append(health.Issues, "NFS share not found for path "+sharePath)
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	shareOK := true
	if !share.Enabled {
		health.Issues = append(health.Issues, "NFS share is disabled")
		shareOK = false
	}
	health.ShareOK = &shareOK
}

// checkNVMeOFHealth checks NVMe-oF-specific health for a volume.
func checkNVMeOFHealth(ds *tnsapi.DatasetWithProperties, nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem, health *VolumeHealth) {
	// Get expected subsystem NQN
	nqn := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

	if nqn == "" {
		health.Issues = append(health.Issues, "NVMe-oF subsystem NQN not found in properties")
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	// Check if subsystem exists (try both name and full NQN)
	// Note: we only check existence, not the Enabled field — TrueNAS NVMe-oF
	// subsystems function normally regardless of that flag, and the driver
	// does not set it during creation.
	_, exists := nvmeSubsysMap[nqn]
	if !exists {
		health.Issues = append(health.Issues, "NVMe-oF subsystem not found: "+nqn)
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	subsysOK := true
	health.SubsysOK = &subsysOK
}

// outputHealthReport outputs the health report in the specified format.
func outputHealthReport(report *HealthReport, format string, showAll bool) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if showAll {
			return enc.Encode(report)
		}
		// Just encode problems
		return enc.Encode(map[string]interface{}{
			"summary":  report.Summary,
			"problems": report.Problems,
		})

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if showAll {
			return enc.Encode(report)
		}
		return enc.Encode(map[string]interface{}{
			"summary":  report.Summary,
			"problems": report.Problems,
		})

	case outputFormatTable, "":
		return outputHealthReportTable(report, showAll)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// outputHealthReportTable outputs the health report in table format.
func outputHealthReportTable(report *HealthReport, showAll bool) error {
	// Summary
	colorHeader.Println("=== Health Summary ===") //nolint:errcheck,gosec
	fmt.Printf("Total Volumes:    %d\n", report.Summary.TotalVolumes)
	fmt.Printf("Healthy:          %s\n", colorSuccess.Sprintf("%d", report.Summary.HealthyVolumes))
	fmt.Printf("Degraded:         %s\n", colorWarning.Sprintf("%d", report.Summary.DegradedVolumes))
	fmt.Printf("Unhealthy:        %s\n", colorError.Sprintf("%d", report.Summary.UnhealthyVolumes))
	fmt.Println()

	// Determine which volumes to show
	volumes := report.Problems
	if showAll {
		volumes = report.Volumes
	}

	if len(volumes) == 0 {
		if showAll {
			fmt.Println("No volumes found.")
		} else {
			colorSuccess.Println("All volumes are healthy!") //nolint:errcheck,gosec
		}
		return nil
	}

	// Volume details
	if showAll {
		colorHeader.Println("=== All Volumes ===") //nolint:errcheck,gosec
	} else {
		colorHeader.Println("=== Volumes with Issues ===") //nolint:errcheck,gosec
	}

	t := newStyledTable()
	t.AppendHeader(table.Row{"VOLUME_ID", "PROTOCOL", "STATUS", "ISSUES"})

	for i := range volumes {
		v := &volumes[i]
		issues := colorMuted.Sprint("-")
		if len(v.Issues) > 0 {
			issues = v.Issues[0]
			if len(v.Issues) > 1 {
				issues = fmt.Sprintf("%s (+%d more)", issues, len(v.Issues)-1)
			}
		}
		var statusStr string
		switch v.Status {
		case HealthStatusHealthy:
			statusStr = colorSuccess.Sprint(v.Status)
		case HealthStatusDegraded:
			statusStr = colorWarning.Sprint(v.Status)
		case HealthStatusUnhealthy:
			statusStr = colorError.Sprint(v.Status)
		default:
			statusStr = string(v.Status)
		}
		t.AppendRow(table.Row{v.VolumeID, protocolBadge(v.Protocol), statusStr, issues})
	}

	renderTable(t)
	return nil
}
