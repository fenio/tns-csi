// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// ErrDatasetDeleteTimeout is returned when waiting for a dataset to be deleted times out.
var ErrDatasetDeleteTimeout = errors.New("timeout waiting for dataset to be deleted")

// TrueNASVerifier provides methods for verifying TrueNAS backend state.
type TrueNASVerifier struct {
	client *tnsapi.Client
}

// NewTrueNASVerifier creates a new TrueNASVerifier.
func NewTrueNASVerifier(host, apiKey string) (*TrueNASVerifier, error) {
	url := fmt.Sprintf("wss://%s/api/current", host)
	client, err := tnsapi.NewClient(url, apiKey, true) // skip TLS verify for tests
	if err != nil {
		return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	return &TrueNASVerifier{client: client}, nil
}

// Close closes the TrueNAS client connection.
func (v *TrueNASVerifier) Close() {
	if v.client != nil {
		v.client.Close()
	}
}

// DatasetExists checks if a dataset exists on TrueNAS.
func (v *TrueNASVerifier) DatasetExists(ctx context.Context, datasetPath string) (bool, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter}, &datasets); err != nil {
		return false, fmt.Errorf("failed to query dataset: %w", err)
	}
	return len(datasets) > 0, nil
}

// WaitForDatasetDeleted polls TrueNAS until the dataset is confirmed deleted or timeout.
func (v *TrueNASVerifier) WaitForDatasetDeleted(ctx context.Context, datasetPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		exists, err := v.DatasetExists(ctx, datasetPath)
		if err != nil {
			// Log but continue polling - transient errors are possible
			fmt.Printf("Warning: error checking dataset existence: %v\n", err)
		} else if !exists {
			return nil // Dataset is deleted
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
			// Continue polling
		}
	}

	return fmt.Errorf("%w: %s", ErrDatasetDeleteTimeout, datasetPath)
}

// NFSShareExists checks if an NFS share exists for the given path.
func (v *TrueNASVerifier) NFSShareExists(ctx context.Context, path string) (bool, error) {
	var shares []map[string]any
	filter := []any{[]any{"path", "=", path}}
	if err := v.client.Call(ctx, "sharing.nfs.query", []any{filter}, &shares); err != nil {
		return false, fmt.Errorf("failed to query NFS shares: %w", err)
	}
	return len(shares) > 0, nil
}

// NVMeOFSubsystemExists checks if an NVMe-oF subsystem exists with the given NQN.
func (v *TrueNASVerifier) NVMeOFSubsystemExists(ctx context.Context, nqn string) (bool, error) {
	var subsystems []map[string]any
	filter := []any{[]any{"nqn", "=", nqn}}
	if err := v.client.Call(ctx, "nvmeof.subsystem.query", []any{filter}, &subsystems); err != nil {
		return false, fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
	}
	return len(subsystems) > 0, nil
}
