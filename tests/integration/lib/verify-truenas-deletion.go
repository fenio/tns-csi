// Package main provides a helper tool to verify that a dataset/zvol was
// actually deleted from TrueNAS. Used by PVC lifecycle tests to confirm
// backend cleanup after PVC deletion.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: verify-truenas-deletion <volume-handle> [timeout-seconds]")
		fmt.Println("")
		fmt.Println("Verifies that a dataset/zvol does NOT exist on TrueNAS.")
		fmt.Println("Exits 0 if deleted (not found), exits 1 if still exists or error.")
		fmt.Println("")
		fmt.Println("Environment variables required:")
		fmt.Println("  TRUENAS_HOST    - TrueNAS hostname or IP")
		fmt.Println("  TRUENAS_API_KEY - TrueNAS API key")
		os.Exit(1)
	}

	volumeHandle := os.Args[1]
	timeout := 30 // default timeout in seconds

	if len(os.Args) >= 3 {
		if parsed, err := strconv.Atoi(os.Args[2]); err == nil {
			timeout = parsed
		}
	}

	host := os.Getenv("TRUENAS_HOST")
	apiKey := os.Getenv("TRUENAS_API_KEY")

	if host == "" || apiKey == "" {
		fmt.Println("ERROR: TRUENAS_HOST and TRUENAS_API_KEY environment variables are required")
		os.Exit(1)
	}

	// Construct WebSocket URL
	url := fmt.Sprintf("wss://%s/api/current", host)

	fmt.Printf("Connecting to TrueNAS at %s...\n", url)

	client, err := tnsapi.NewClient(url, apiKey, true)
	if err != nil {
		fmt.Printf("ERROR: Failed to create client: %v\n", err)
		os.Exit(1)
	}

	// Run the verification and ensure we close the client before exiting
	exitCode := verifyDeletion(client, volumeHandle, timeout)
	client.Close()
	os.Exit(exitCode)
}

// verifyDeletion polls TrueNAS to verify a dataset was deleted.
// Returns 0 if deleted, 1 if still exists or error.
func verifyDeletion(client *tnsapi.Client, datasetPath string, timeout int) int {
	ctx := context.Background()

	fmt.Printf("Checking if dataset/zvol exists: %s\n", datasetPath)
	fmt.Printf("Timeout: %d seconds\n", timeout)

	// Poll for deletion with timeout
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollInterval := 2 * time.Second
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++

		// Try to get the dataset
		dataset, err := client.Dataset(ctx, datasetPath)

		switch {
		case err != nil:
			// Check if error indicates "not found"
			errStr := err.Error()
			if strings.Contains(errStr, "does not exist") ||
				strings.Contains(errStr, "not found") ||
				strings.Contains(errStr, "ENOENT") ||
				strings.Contains(errStr, "dataset.query") {

				fmt.Printf("SUCCESS: Dataset '%s' confirmed deleted from TrueNAS (attempt %d)\n", datasetPath, attempt)
				return 0
			}
			// Some other error - log but continue polling
			fmt.Printf("Attempt %d: Error querying dataset: %v\n", attempt, err)

		case dataset == nil:
			fmt.Printf("SUCCESS: Dataset '%s' not found on TrueNAS (attempt %d)\n", datasetPath, attempt)
			return 0

		default:
			fmt.Printf("Attempt %d: Dataset still exists: %s (type: %s)\n", attempt, dataset.Name, dataset.Type)
		}

		time.Sleep(pollInterval)
	}

	// Timeout reached - dataset still exists
	fmt.Printf("FAILED: Dataset '%s' still exists on TrueNAS after %d seconds\n", datasetPath, timeout)
	fmt.Printf("This indicates the CSI DeleteVolume did not properly clean up the backend resource.\n")
	return 1
}
