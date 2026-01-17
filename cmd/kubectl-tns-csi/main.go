// Package main implements the kubectl-tns-csi plugin for managing TrueNAS CSI volumes.
//
// Installation:
//
//	go build -o kubectl-tns_csi ./cmd/kubectl-tns-csi
//	mv kubectl-tns_csi /usr/local/bin/  # or anywhere in PATH
//
// Usage:
//
//	kubectl tns-csi list                     # List all tns-csi managed volumes
//	kubectl tns-csi list-orphaned            # Find volumes with no matching PVC
//	kubectl tns-csi adopt <dataset-path>     # Generate static PV manifest
//	kubectl tns-csi status <pvc-name>        # Show volume status from TrueNAS
//	kubectl tns-csi connectivity             # Test TrueNAS connection
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// Build information (set via ldflags).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		truenasURL    string
		truenasAPIKey string
		secretRef     string
		outputFormat  string
		skipTLSVerify bool
	)

	rootCmd := &cobra.Command{
		Use:   "kubectl-tns-csi",
		Short: "Manage TrueNAS CSI volumes",
		Long: `kubectl-tns-csi is a kubectl plugin for managing TrueNAS CSI driver volumes.

It provides commands for discovering orphaned volumes, adopting volumes across
clusters, and troubleshooting volume issues.

Connection to TrueNAS can be configured via:
  - Flags: --url and --api-key
  - Kubernetes secret: --secret <namespace>/<name>
  - Environment: TRUENAS_URL and TRUENAS_API_KEY`,
		Version: version + " (" + commit + ")",
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&truenasURL, "url", "", "TrueNAS WebSocket URL (wss://host/api/current)")
	rootCmd.PersistentFlags().StringVar(&truenasAPIKey, "api-key", "", "TrueNAS API key")
	rootCmd.PersistentFlags().StringVar(&secretRef, "secret", "", "Kubernetes secret with TrueNAS credentials (namespace/name)")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table, yaml, json")
	rootCmd.PersistentFlags().BoolVar(&skipTLSVerify, "insecure-skip-tls-verify", true, "Skip TLS certificate verification")

	// Add subcommands
	rootCmd.AddCommand(newListCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newListSnapshotsCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newListOrphanedCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newDescribeCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newHealthCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newTroubleshootCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newSummaryCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newCleanupCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newAdoptCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newStatusCmd(&truenasURL, &truenasAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newConnectivityCmd(&truenasURL, &truenasAPIKey, &secretRef, &skipTLSVerify))

	return rootCmd
}
