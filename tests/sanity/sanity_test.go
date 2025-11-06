package sanity

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fenio/tns-csi/pkg/driver"
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
	// Create temporary directory
	tmpDir := t.TempDir()
	stagingPath := filepath.Join(tmpDir, "staging")
	targetPath := filepath.Join(tmpDir, "target")

	// Create a temporary socket file
	sockPath := filepath.Join(tmpDir, "csi-sanity.sock")
	endpoint := "unix://" + sockPath

	// Create mock client
	mockClient := NewMockClient()

	// Create driver configuration
	cfg := driver.Config{
		DriverName: driverName,
		Version:    driverVersion,
		NodeID:     nodeID,
		Endpoint:   endpoint,
	}

	// Create driver with mock client
	drv, err := driver.NewDriverWithClient(cfg, mockClient)
	if err != nil {
		t.Fatalf("Failed to create driver: %v", err)
	}

	// Start driver in a goroutine
	driverStarted := make(chan struct{})
	driverErr := make(chan error, 1)

	go func() {
		close(driverStarted)
		if err := drv.Run(); err != nil {
			driverErr <- err
		}
	}()

	// Wait for driver to start
	<-driverStarted
	time.Sleep(100 * time.Millisecond) // Give the driver time to bind to socket

	// Check for early driver errors
	select {
	case err := <-driverErr:
		t.Fatalf("Driver failed to start: %v", err)
	default:
	}

	// Configure sanity test
	sanityCfg := sanity.NewTestConfig()
	sanityCfg.Address = endpoint
	sanityCfg.StagingPath = stagingPath
	sanityCfg.TargetPath = targetPath
	sanityCfg.TestVolumeSize = 1 * 1024 * 1024 * 1024 // 1GB

	// Skip Node service tests (require real mounts)
	sanityCfg.TestNodeVolumeAttachLimit = false

	// Configure volume parameters for NFS testing
	sanityCfg.TestVolumeParameters = map[string]string{
		"protocol": "nfs",
		"pool":     "tank",
	}

	// Run sanity tests
	sanity.Test(t, sanityCfg)

	// Cleanup
	drv.Stop()
}

// TestSanityIdentity runs only Identity service sanity tests.
// These tests don't require a TrueNAS backend and can run immediately.
func TestSanityIdentity(t *testing.T) {
	t.Skip("Identity tests are covered by TestSanity - skipping separate test")
}

// TestSanityController runs only Controller service sanity tests.
// These tests use the mock client and don't touch actual storage.
func TestSanityController(t *testing.T) {
	t.Skip("Controller tests are covered by TestSanity - skipping separate test")
}
