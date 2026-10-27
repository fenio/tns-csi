package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsAvailability(t *testing.T) {
	// Record some sample metrics to ensure they appear in output
	RecordCSIOperation(OpCreateVolume, "success", 100*time.Millisecond)
	RecordVolumeOperation(ProtocolNFS, "create", "success", 200*time.Millisecond)
	SetWSConnectionStatus(true)
	RecordWSReconnection()
	RecordWSMessage("sent")
	RecordWSMessageDuration("pool.dataset.create", 100*time.Millisecond)
	SetWSConnectionDuration(5 * time.Minute)
	SetVolumeCapacity("test-vol", ProtocolNFS, 1024*1024*1024)

	// Create a test HTTP server with the metrics handler
	server := httptest.NewServer(promhttp.Handler())
	defer server.Close()

	// Make a request to the metrics endpoint
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	content := string(body)

	// Verify that our custom metrics are present
	expectedMetrics := []string{
		"tns_csi_operations_total",
		"tns_csi_operation_duration_seconds",
		"tns_csi_volume_operations_total",
		"tns_csi_volume_operation_duration_seconds",
		"tns_csi_websocket_connection_status",
		"tns_csi_websocket_reconnections_total",
		"tns_csi_websocket_messages_total",
		"tns_csi_websocket_message_duration_seconds",
		"tns_csi_websocket_connection_duration_seconds",
		"tns_csi_volume_capacity_bytes",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(content, metric) {
			t.Errorf("Expected metric %s not found in metrics output", metric)
		}
	}

	// Clean up
	DeleteVolumeCapacity("test-vol", ProtocolNFS)
}

func TestRecordCSIOperation(t *testing.T) {
	// Record a successful operation
	RecordCSIOperation(OpCreateVolume, "success", 100*time.Millisecond)

	// Record a failed operation
	RecordCSIOperation(OpDeleteVolume, "error", 50*time.Millisecond)

	// We can't easily verify the values without accessing internal state,
	// but we can verify the function doesn't panic
}

func TestRecordVolumeOperation(t *testing.T) {
	// Record successful NFS operations
	RecordVolumeOperation(ProtocolNFS, "create", "success", 200*time.Millisecond)
	RecordVolumeOperation(ProtocolNFS, "delete", "success", 150*time.Millisecond)

	// Record successful NVMe-oF operations
	RecordVolumeOperation(ProtocolNVMeOF, "create", "success", 300*time.Millisecond)
	RecordVolumeOperation(ProtocolNVMeOF, "expand", "success", 250*time.Millisecond)

	// Record failed operations
	RecordVolumeOperation(ProtocolNFS, "create", "error", 100*time.Millisecond)
}

func TestWebSocketMetrics(t *testing.T) {
	// Test connection status
	SetWSConnectionStatus(true)
	SetWSConnectionStatus(false)

	// Test reconnection counter
	RecordWSReconnection()
	RecordWSReconnection()

	// Test message counters
	RecordWSMessage("sent")
	RecordWSMessage("received")

	// Test message duration
	RecordWSMessageDuration("pool.dataset.create", 100*time.Millisecond)

	// Test connection duration
	SetWSConnectionDuration(5 * time.Minute)
}

func TestVolumeCapacityMetrics(t *testing.T) {
	// Set volume capacity
	SetVolumeCapacity("vol-123", ProtocolNFS, 1024*1024*1024) // 1GB

	// Update volume capacity
	SetVolumeCapacity("vol-123", ProtocolNFS, 2*1024*1024*1024) // 2GB

	// Delete volume capacity
	DeleteVolumeCapacity("vol-123", ProtocolNFS)
}

func TestOperationTimer(t *testing.T) {
	// Test CSI operation timer
	timer := NewOperationTimer(OpCreateVolume)
	time.Sleep(10 * time.Millisecond)
	timer.ObserveSuccess()

	timer2 := NewOperationTimer(OpDeleteVolume)
	time.Sleep(5 * time.Millisecond)
	timer2.ObserveError()

	// Test volume operation timer
	volTimer := NewVolumeOperationTimer(ProtocolNFS, "create")
	time.Sleep(10 * time.Millisecond)
	volTimer.ObserveSuccess()

	volTimer2 := NewVolumeOperationTimer(ProtocolNVMeOF, "delete")
	time.Sleep(5 * time.Millisecond)
	volTimer2.ObserveError()
}

func TestWSMessageTimer(t *testing.T) {
	timer := NewWSMessageTimer("pool.dataset.query")
	time.Sleep(10 * time.Millisecond)
	timer.Observe()

	timer2 := NewWSMessageTimer("sharing.nfs.create")
	time.Sleep(5 * time.Millisecond)
	timer2.Observe()
}

func TestMetricsConstants(t *testing.T) {
	// Verify operation constants are set
	if OpCreateVolume == "" {
		t.Error("OpCreateVolume should not be empty")
	}

	// Verify protocol constants
	if ProtocolNFS == "" || ProtocolNVMeOF == "" {
		t.Error("Protocol constants should not be empty")
	}
}

func TestSyncVolumeCapacities(t *testing.T) {
	stale := VolumeCapacityKey{VolumeID: "tank/csi/stale", Protocol: ProtocolNFS}
	kept := VolumeCapacityKey{VolumeID: "tank/csi/kept", Protocol: ProtocolISCSI}
	created := VolumeCapacityKey{VolumeID: "tank/csi/created-during-listing", Protocol: ProtocolSMB}
	listed := VolumeCapacityKey{VolumeID: "tank/csi/listed", Protocol: ProtocolNVMeOF}
	t.Cleanup(func() {
		for _, key := range []VolumeCapacityKey{stale, kept, created, listed} {
			DeleteVolumeCapacity(key.VolumeID, key.Protocol)
		}
	})

	SetVolumeCapacity(stale.VolumeID, stale.Protocol, 1)
	SetVolumeCapacity(kept.VolumeID, kept.Protocol, 2)
	before := VolumeCapacitySnapshot()
	// Created after the snapshot, so a sync that started before it must keep it.
	SetVolumeCapacity(created.VolumeID, created.Protocol, 3)

	SyncVolumeCapacities(before, map[VolumeCapacityKey]int64{kept: 20, listed: 40})

	want := map[VolumeCapacityKey]float64{kept: 20, created: 3, listed: 40}
	if got := testutil.CollectAndCount(volumeCapacityBytes); got != len(want) {
		t.Errorf("volume_capacity_bytes has %d series, want %d", got, len(want))
	}
	for key, value := range want {
		if got := testutil.ToFloat64(volumeCapacityBytes.WithLabelValues(key.VolumeID, key.Protocol)); got != value {
			t.Errorf("volume_capacity_bytes{%s,%s} = %v, want %v", key.VolumeID, key.Protocol, got, value)
		}
	}
	if _, ok := VolumeCapacitySnapshot()[stale]; ok {
		t.Errorf("stale series %v is still tracked", stale)
	}
}
