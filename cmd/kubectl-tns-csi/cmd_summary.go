package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Icon constants for status display.
const (
	iconOK      = "✓"
	iconError   = "✗"
	iconWarning = "!"
)

// Summary contains the overall summary of tns-csi managed resources.
//
//nolint:govet // field alignment not critical for CLI output struct
type Summary struct {
	Volumes   VolumeSummary   `json:"volumes"   yaml:"volumes"`
	Snapshots SnapshotSummary `json:"snapshots" yaml:"snapshots"`
	Capacity  CapacitySummary `json:"capacity"  yaml:"capacity"`
	Health    HealthSummary   `json:"health"    yaml:"health"`
}

// VolumeSummary contains volume statistics.
type VolumeSummary struct {
	Total  int `json:"total"  yaml:"total"`
	NFS    int `json:"nfs"    yaml:"nfs"`
	NVMeOF int `json:"nvmeof" yaml:"nvmeof"`
	Clones int `json:"clones" yaml:"clones"`
}

// SnapshotSummary contains snapshot statistics.
type SnapshotSummary struct {
	Total    int `json:"total"    yaml:"total"`
	Attached int `json:"attached" yaml:"attached"`
	Detached int `json:"detached" yaml:"detached"`
}

// CapacitySummary contains capacity statistics.
//
//nolint:govet // field alignment not critical for CLI output struct
type CapacitySummary struct {
	ProvisionedBytes int64  `json:"provisionedBytes" yaml:"provisionedBytes"`
	ProvisionedHuman string `json:"provisionedHuman" yaml:"provisionedHuman"`
	UsedBytes        int64  `json:"usedBytes"        yaml:"usedBytes"`
	UsedHuman        string `json:"usedHuman"        yaml:"usedHuman"`
}

// Note: HealthSummary is already defined in cmd_health.go

func newSummaryCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show summary of all tns-csi managed resources",
		Long: `Display a dashboard-style summary of all tns-csi managed resources.

Shows:
  - Volume counts by protocol (NFS, NVMe-oF)
  - Snapshot counts (attached vs detached)
  - Total capacity (provisioned and used)
  - Health status breakdown

Examples:
  # Show summary
  kubectl tns-csi summary

  # Output as JSON for scripting
  kubectl tns-csi summary -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSummary(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runSummary(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
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

	// Gather summary
	summary, err := gatherSummary(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to gather summary: %w", err)
	}

	// Output based on format
	return outputSummary(summary, *outputFormat)
}

// summaryContext holds data needed for summary collection.
type summaryContext struct {
	client        tnsapi.ClientInterface
	nfsShareMap   map[string]*tnsapi.NFSShare
	nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem
}

// gatherSummary collects all summary statistics.
func gatherSummary(ctx context.Context, client tnsapi.ClientInterface) (*Summary, error) {
	summary := &Summary{}

	// Get all managed datasets (volumes)
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	// Build lookup maps for health checks
	sc := buildSummaryContext(ctx, client)

	// Process volume datasets
	processVolumeDatasets(datasets, sc, summary)

	// Count attached snapshots
	countAttachedSnapshots(ctx, client, datasets, summary)

	// Finalize summary
	summary.Snapshots.Total = summary.Snapshots.Attached + summary.Snapshots.Detached
	summary.Capacity.ProvisionedHuman = formatBytes(summary.Capacity.ProvisionedBytes)
	summary.Capacity.UsedHuman = formatBytes(summary.Capacity.UsedBytes)
	summary.Health.TotalVolumes = summary.Volumes.Total

	return summary, nil
}

// buildSummaryContext creates lookup maps for NFS shares and NVMe subsystems.
func buildSummaryContext(ctx context.Context, client tnsapi.ClientInterface) *summaryContext {
	sc := &summaryContext{
		client:        client,
		nfsShareMap:   make(map[string]*tnsapi.NFSShare),
		nvmeSubsysMap: make(map[string]*tnsapi.NVMeOFSubsystem),
	}

	// Get all NFS shares for health checks (ignore errors - non-critical)
	nfsShares, _ := client.QueryAllNFSShares(ctx, "") //nolint:errcheck // non-critical for summary
	for i := range nfsShares {
		sc.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}

	// Get all NVMe-oF subsystems for health checks (ignore errors - non-critical)
	nvmeSubsystems, _ := client.ListAllNVMeOFSubsystems(ctx) //nolint:errcheck // non-critical for summary
	for i := range nvmeSubsystems {
		sc.nvmeSubsysMap[nvmeSubsystems[i].Name] = &nvmeSubsystems[i]
		sc.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}

	return sc
}

// processVolumeDatasets processes all datasets and updates summary counters.
func processVolumeDatasets(datasets []tnsapi.DatasetWithProperties, sc *summaryContext, summary *Summary) {
	for i := range datasets {
		ds := &datasets[i]

		// Skip detached snapshots (they're counted separately)
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			summary.Snapshots.Detached++
			continue
		}

		// Skip datasets without volume ID (not actual volumes)
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID == "" {
			continue
		}

		// Count and categorize this volume
		processVolume(ds, sc, summary)
	}
}

// processVolume processes a single volume dataset.
func processVolume(ds *tnsapi.DatasetWithProperties, sc *summaryContext, summary *Summary) {
	summary.Volumes.Total++

	// Get protocol
	protocol := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
		protocol = prop.Value
	}

	// Count by protocol
	switch protocol {
	case protocolNFS:
		summary.Volumes.NFS++
	case protocolNVMeOF:
		summary.Volumes.NVMeOF++
	}

	// Check if it's a clone
	if prop, ok := ds.UserProperties[tnsapi.PropertyContentSourceType]; ok && prop.Value != "" {
		summary.Volumes.Clones++
	}

	// Add capacity
	if prop, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
		summary.Capacity.ProvisionedBytes += tnsapi.StringToInt64(prop.Value)
	}

	// Add used space
	if ds.Used != nil {
		if val, ok := ds.Used["parsed"].(float64); ok {
			summary.Capacity.UsedBytes += int64(val)
		}
	}

	// Check health
	healthy := checkVolumeHealthForSummary(ds, protocol, sc)
	if healthy {
		summary.Health.HealthyVolumes++
	} else {
		summary.Health.UnhealthyVolumes++
	}
}

// checkVolumeHealthForSummary checks if a volume is healthy based on protocol.
func checkVolumeHealthForSummary(ds *tnsapi.DatasetWithProperties, protocol string, sc *summaryContext) bool {
	switch protocol {
	case protocolNFS:
		return checkNFSHealthForSummary(ds, sc.nfsShareMap)
	case protocolNVMeOF:
		return checkNVMeOFHealthForSummary(ds, sc.nvmeSubsysMap)
	default:
		return true // Unknown protocol - assume healthy
	}
}

// countAttachedSnapshots counts ZFS snapshots on managed datasets.
func countAttachedSnapshots(ctx context.Context, client tnsapi.ClientInterface, datasets []tnsapi.DatasetWithProperties, summary *Summary) {
	for i := range datasets {
		ds := &datasets[i]

		// Skip detached snapshots
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}
		// Skip non-volumes
		if _, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; !ok {
			continue
		}

		// Count snapshots on this dataset
		filter := []interface{}{
			[]interface{}{"id", "^", ds.ID + "@"},
		}
		snapshots, err := client.QuerySnapshots(ctx, filter)
		if err != nil {
			continue
		}

		for j := range snapshots {
			snap := &snapshots[j]
			if isManagedSnapshot(snap) {
				summary.Snapshots.Attached++
			}
		}
	}
}

// isManagedSnapshot checks if a snapshot is managed by tns-csi.
func isManagedSnapshot(snap *tnsapi.Snapshot) bool {
	prop, ok := snap.Properties[tnsapi.PropertyManagedBy]
	if !ok {
		return false
	}
	propMap, ok := prop.(map[string]interface{})
	if !ok {
		return false
	}
	val, ok := propMap["value"].(string)
	return ok && val == tnsapi.ManagedByValue
}

// checkNFSHealthForSummary checks if NFS volume is healthy.
func checkNFSHealthForSummary(ds *tnsapi.DatasetWithProperties, nfsShareMap map[string]*tnsapi.NFSShare) bool {
	sharePath := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
	}

	if sharePath == "" {
		return false
	}

	share, exists := nfsShareMap[sharePath]
	if !exists {
		return false
	}

	return share.Enabled
}

// checkNVMeOFHealthForSummary checks if NVMe-oF volume is healthy.
func checkNVMeOFHealthForSummary(ds *tnsapi.DatasetWithProperties, nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem) bool {
	nqn := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

	if nqn == "" {
		return false
	}

	subsystem, exists := nvmeSubsysMap[nqn]
	if !exists {
		return false
	}

	return subsystem.Enabled
}

// outputSummary outputs the summary in the specified format.
func outputSummary(summary *Summary, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(summary)

	case outputFormatTable, "":
		return outputSummaryTable(summary)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// boxWidth is the inner width of the summary box (between the borders).
const boxWidth = 62

// printBoxLine prints a line with box borders and proper padding.
func printBoxLine(content string) {
	// Calculate padding needed
	padding := boxWidth - len(content)
	if padding < 0 {
		padding = 0
		content = content[:boxWidth]
	}
	fmt.Printf("║ %s%*s ║\n", content, padding, "")
}

// outputSummaryTable outputs the summary in a nice table format.
func outputSummaryTable(summary *Summary) error {
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                        TNS-CSI Summary                         ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")

	// Volumes section
	printBoxLine("VOLUMES")
	printBoxLine(fmt.Sprintf("  Total: %-6d NFS: %-6d NVMe-oF: %-6d Clones: %d",
		summary.Volumes.Total, summary.Volumes.NFS, summary.Volumes.NVMeOF, summary.Volumes.Clones))

	fmt.Println("╠────────────────────────────────────────────────────────────────╣")

	// Snapshots section
	printBoxLine("SNAPSHOTS")
	printBoxLine(fmt.Sprintf("  Total: %-6d Attached: %-6d Detached: %d",
		summary.Snapshots.Total, summary.Snapshots.Attached, summary.Snapshots.Detached))

	fmt.Println("╠────────────────────────────────────────────────────────────────╣")

	// Capacity section
	printBoxLine("CAPACITY")

	// Calculate usage percentage
	usagePercent := 0.0
	if summary.Capacity.ProvisionedBytes > 0 {
		usagePercent = float64(summary.Capacity.UsedBytes) / float64(summary.Capacity.ProvisionedBytes) * 100
	}
	printBoxLine(fmt.Sprintf("  Provisioned: %-10s Used: %-10s (%.1f%%)",
		summary.Capacity.ProvisionedHuman, summary.Capacity.UsedHuman, usagePercent))

	fmt.Println("╠────────────────────────────────────────────────────────────────╣")

	// Health section
	printBoxLine("HEALTH")

	// Build health status with icons
	healthIcon := iconOK
	if summary.Health.UnhealthyVolumes > 0 {
		healthIcon = iconError
	} else if summary.Health.DegradedVolumes > 0 {
		healthIcon = iconWarning
	}

	healthLine := fmt.Sprintf("  %s Healthy: %d", healthIcon, summary.Health.HealthyVolumes)
	if summary.Health.DegradedVolumes > 0 {
		healthLine += fmt.Sprintf("  Degraded: %d", summary.Health.DegradedVolumes)
	}
	if summary.Health.UnhealthyVolumes > 0 {
		healthLine += fmt.Sprintf("  Unhealthy: %d", summary.Health.UnhealthyVolumes)
	}
	printBoxLine(healthLine)

	fmt.Println("╚════════════════════════════════════════════════════════════════╝")

	// Show warning if there are unhealthy volumes
	if summary.Health.UnhealthyVolumes > 0 {
		fmt.Println()
		fmt.Printf("⚠  %d volume(s) unhealthy. Run 'kubectl tns-csi health' for details.\n", summary.Health.UnhealthyVolumes)
	}

	return nil
}
