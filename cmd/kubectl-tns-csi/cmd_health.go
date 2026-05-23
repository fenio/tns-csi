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
	report, err := dashboard.CheckVolumeHealth(ctx, client)
	spin.stop()
	if err != nil {
		return fmt.Errorf("failed to check health: %w", err)
	}

	// Output based on format
	return outputHealthReport(report, *outputFormat, showAll)
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
			summaryName: report.Summary,
			"problems":  report.Problems,
		})

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if showAll {
			return enc.Encode(report)
		}
		return enc.Encode(map[string]interface{}{
			summaryName: report.Summary,
			"problems":  report.Problems,
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
	t.AppendHeader(table.Row{colVolumeID, colProtocol, "STATUS", "ISSUES"})

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
		case dashboard.HealthStatusHealthy:
			statusStr = colorSuccess.Sprint(v.Status)
		case dashboard.HealthStatusDegraded:
			statusStr = colorWarning.Sprint(v.Status)
		case dashboard.HealthStatusUnhealthy:
			statusStr = colorError.Sprint(v.Status)
		default:
			statusStr = string(v.Status)
		}
		t.AppendRow(table.Row{v.VolumeID, protocolBadge(v.Protocol), statusStr, issues})
	}

	renderTable(t)
	return nil
}
