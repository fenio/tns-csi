package sanity

import (
	"os"
	"path/filepath"
	"testing"

	sanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
)

const (
	driverName    = "tns.csi.io"
	driverVersion = "test"
	nodeID        = "test-node"
	endpoint      = "unix:///tmp/csi-sanity.sock"
)

// TestSanity runs the CSI sanity test suite against the TNS CSI driver.
func TestSanity(t *testing.T) {
	// Create temporary directories for staging and target paths
	tmpDir := t.TempDir()
	stagingPath := filepath.Join(tmpDir, "staging")
	targetPath := filepath.Join(tmpDir, "target")

	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		t.Fatalf("Failed to create staging path: %v", err)
	}
	if err := os.MkdirAll(targetPath, 0750); err != nil {
		t.Fatalf("Failed to create target path: %v", err)
	}

	// Note: For now, we skip Node service tests as they require real mount operations
	// This focuses on Controller and Identity services which can be fully mocked
	
	// Create mock client adapter
	// TODO: Implement proper adapter between MockClient and tnsapi.Client interface
	// For now, we'll need to modify the driver to accept an interface instead of concrete type
	
	// Configure sanity test
	cfg := sanity.NewTestConfig()
	cfg.Address = endpoint
	cfg.TestVolumeSize = 1 * 1024 * 1024 * 1024 // 1GB
	cfg.StagingPath = stagingPath
	cfg.TargetPath = targetPath
	
	// Skip Node service tests (require real mounts)
	cfg.TestNodeVolumeAttachLimit = false
	
	// Configure volume parameters for NFS testing
	cfg.TestVolumeParameters = map[string]string{
		"protocol": "nfs",
		"pool":     "tank",
	}

	// Create and start the driver in a goroutine
	// Note: This requires the driver to be modified to support dependency injection
	// of the TrueNAS client for testing purposes
	
	t.Skip("Skipping sanity tests - requires driver refactoring to support mock client injection")

	// Once refactoring is complete, uncomment:
	/*
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	mockClient := NewMockClient()
	
	driverCfg := driver.Config{
		DriverName:  driverName,
		Version:     driverVersion,
		NodeID:      nodeID,
		Endpoint:    endpoint,
		MetricsAddr: "", // Disable metrics for tests
	}
	
	drv, err := driver.NewDriverWithClient(driverCfg, mockClient)
	if err != nil {
		t.Fatalf("Failed to create driver: %v", err)
	}

	go func() {
		if runErr := drv.Run(); runErr != nil {
			t.Errorf("Driver failed: %v", runErr)
		}
	}()
	defer drv.Stop()

	// Give driver time to start
	time.Sleep(100 * time.Millisecond)

	// Run sanity tests
	sanity.Test(t, cfg)
	*/
}

// TestSanityIdentity runs only Identity service sanity tests.
// These tests don't require a TrueNAS backend and can run immediately.
func TestSanityIdentity(t *testing.T) {
	t.Skip("Requires driver running - will be enabled after refactoring")
	
	// This test would validate:
	// - GetPluginInfo
	// - GetPluginCapabilities
	// - Probe
}

// TestSanityController runs only Controller service sanity tests.
// These tests use the mock client and don't touch actual storage.
func TestSanityController(t *testing.T) {
	t.Skip("Requires mock client integration - will be enabled after refactoring")
	
	// This test would validate:
	// - CreateVolume / DeleteVolume
	// - ControllerGetCapabilities
	// - ValidateVolumeCapabilities
	// - ListVolumes
	// - GetCapacity
	// - ControllerExpandVolume
	// - CreateSnapshot / DeleteSnapshot
	// - ListSnapshots
}
