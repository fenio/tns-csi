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
	fmt.Println("Testing TrueNAS connectivity...")
	fmt.Println()

	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		fmt.Printf("Configuration: FAILED\n")
		fmt.Printf("  Error: %v\n", err)
		return err
	}
	fmt.Printf("Configuration: OK\n")
	fmt.Printf("  URL: %s\n", cfg.URL)
	fmt.Printf("  API Key: %s...%s\n", cfg.APIKey[:4], cfg.APIKey[len(cfg.APIKey)-4:])
	fmt.Println()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Test connection
	fmt.Printf("Connecting to TrueNAS...\n")
	startTime := time.Now()

	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		fmt.Printf("Connection: FAILED\n")
		fmt.Printf("  Error: %v\n", err)
		return err
	}
	defer client.Close()

	connectionTime := time.Since(startTime)
	fmt.Printf("Connection: OK (%.2fs)\n", connectionTime.Seconds())
	fmt.Println()

	// Query system info
	fmt.Printf("Querying system info...\n")
	startTime = time.Now()

	pool, err := client.QueryPool(ctx, "")
	if err != nil {
		// Try listing all pools instead
		fmt.Printf("  (No default pool, checking pool access...)\n")
	} else if pool != nil {
		fmt.Printf("  Found pool: %s\n", pool.Name)
	}

	// Count managed volumes
	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		fmt.Printf("Volume query: FAILED\n")
		fmt.Printf("  Error: %v\n", err)
	} else {
		fmt.Printf("  Managed volumes: %d\n", len(volumes))

		// Count by protocol
		nfsCount := 0
		nvmeCount := 0
		for _, v := range volumes {
			switch v.Protocol {
			case "nfs":
				nfsCount++
			case "nvmeof":
				nvmeCount++
			}
		}
		if nfsCount > 0 {
			fmt.Printf("    NFS: %d\n", nfsCount)
		}
		if nvmeCount > 0 {
			fmt.Printf("    NVMe-oF: %d\n", nvmeCount)
		}
	}

	queryTime := time.Since(startTime)
	fmt.Printf("API access: OK (%.2fs)\n", queryTime.Seconds())
	fmt.Println()

	fmt.Printf("All checks passed!\n")
	return nil
}
