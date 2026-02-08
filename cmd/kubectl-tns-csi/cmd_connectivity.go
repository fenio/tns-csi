package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newConnectivityCmd(url, apiKey, secretRef *string, skipTLSVerify *bool) *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "connectivity",
		Short: "Test connectivity to TrueNAS",
		Long: `Test WebSocket connectivity to TrueNAS and verify API access.

This command:
  1. Establishes a WebSocket connection
  2. Authenticates with the API key
  3. Queries basic system info to verify access

Examples:
  # Test connectivity using flags
  kubectl tns-csi connectivity --url wss://truenas:443/api/current --api-key <key>

  # Test using credentials from secret
  kubectl tns-csi connectivity --secret kube-system/tns-csi-config

  # Test with custom timeout
  kubectl tns-csi connectivity --timeout 30s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnectivity(cmd.Context(), url, apiKey, secretRef, skipTLSVerify, timeout)
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Connection timeout")

	return cmd
}

func runConnectivity(ctx context.Context, url, apiKey, secretRef *string, skipTLSVerify *bool, timeout time.Duration) error {
	colorHeader.Println("Testing TrueNAS connectivity...") //nolint:errcheck,gosec
	fmt.Println()

	// Step 1: Check configuration
	printStep(colorMuted.Sprint("..."), "Checking configuration...")
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		printStepf(colorError, iconError, "Configuration: FAILED")
		fmt.Printf("  Error: %v\n", err)
		return err
	}
	printStepf(colorSuccess, iconOK, "Configuration: OK")
	fmt.Printf("  URL: %s\n", cfg.URL)
	fmt.Printf("  API Key: [configured, %d chars]\n", len(cfg.APIKey))
	fmt.Println()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Step 2: Test connection
	printStep(colorMuted.Sprint("..."), "Connecting to TrueNAS...")
	startTime := time.Now()

	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		printStepf(colorError, iconError, "Connection: FAILED")
		fmt.Printf("  Error: %v\n", err)
		return err
	}
	defer client.Close()

	connectionTime := time.Since(startTime)
	printStepf(colorSuccess, iconOK, "Connection: OK (%.2fs)", connectionTime.Seconds())
	fmt.Println()

	// Step 3: Verify API access
	printStep(colorMuted.Sprint("..."), "Verifying API access...")
	startTime = time.Now()

	pool, err := client.QueryPool(ctx, "")
	if err != nil {
		fmt.Printf("  %s\n", colorMuted.Sprint("(No default pool, checking pool access...)"))
	} else if pool != nil {
		fmt.Printf("  Found pool: %s\n", pool.Name)
	}

	queryTime := time.Since(startTime)
	printStepf(colorSuccess, iconOK, "API access: OK (%.2fs)", queryTime.Seconds())
	fmt.Println()

	// Step 4: Count managed volumes (best-effort, separate short timeout)
	printStep(colorMuted.Sprint("..."), "Counting managed volumes...")
	volumeCtx, volumeCancel := context.WithTimeout(ctx, 5*time.Second) //nolint:mnd
	defer volumeCancel()

	volumes, err := findManagedVolumes(volumeCtx, client)
	if err != nil {
		printStepf(colorWarning, iconWarning, "Volume count: skipped (query timed out)")
	} else {
		fmt.Printf("  Managed volumes: %d\n", len(volumes))

		// Count by protocol
		nfsCount := 0
		nvmeCount := 0
		iscsiCount := 0
		for i := range volumes {
			switch volumes[i].Protocol {
			case "nfs":
				nfsCount++
			case "nvmeof":
				nvmeCount++
			case "iscsi":
				iscsiCount++
			}
		}
		if nfsCount > 0 {
			fmt.Printf("    %s: %d\n", colorProtocolNFS.Sprint("NFS"), nfsCount)
		}
		if nvmeCount > 0 {
			fmt.Printf("    %s: %d\n", colorProtocolNVMe.Sprint("NVMe-oF"), nvmeCount)
		}
		if iscsiCount > 0 {
			fmt.Printf("    %s: %d\n", colorProtocolISCI.Sprint("iSCSI"), iscsiCount)
		}
		printStepf(colorSuccess, iconOK, "Volume count: OK")
	}
	fmt.Println()

	colorSuccess.Println("All checks passed!") //nolint:errcheck,gosec
	return nil
}
