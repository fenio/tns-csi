package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func managedDataset(id string, props map[string]string) tnsapi.DatasetWithProperties {
	userProps := map[string]tnsapi.UserProperty{
		tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
	}
	for k, v := range props {
		userProps[k] = tnsapi.UserProperty{Value: v}
	}
	return tnsapi.DatasetWithProperties{Dataset: tnsapi.Dataset{ID: id}, UserProperties: userProps}
}

func TestSyncVolumeCapacityMetrics(t *testing.T) {
	stale := metrics.VolumeCapacityKey{VolumeID: "tank/csi/deleted-outside-k8s", Protocol: tnsapi.ProtocolNFS}
	want := map[metrics.VolumeCapacityKey]bool{
		{VolumeID: "tank/csi/pvc-nfs", Protocol: tnsapi.ProtocolNFS}:              true,
		{VolumeID: "tank/csi/pvc-nvme", Protocol: tnsapi.ProtocolNVMeOF}:          true,
		{VolumeID: "tank/csi/pvc-no-protocol", Protocol: metrics.ProtocolUnknown}: true,
	}
	notWant := []metrics.VolumeCapacityKey{
		stale,
		{VolumeID: "tank/csi/pvc-other-cluster", Protocol: tnsapi.ProtocolISCSI},
		{VolumeID: "tank/csi/pvc-unclustered", Protocol: tnsapi.ProtocolISCSI},
		{VolumeID: "tank/csi/snap-detached", Protocol: tnsapi.ProtocolNFS},
		{VolumeID: "tank/csi/pvc-bad-capacity", Protocol: tnsapi.ProtocolSMB},
	}
	t.Cleanup(func() {
		for key := range want {
			metrics.DeleteVolumeCapacity(key.VolumeID, key.Protocol)
		}
		for _, key := range notWant {
			metrics.DeleteVolumeCapacity(key.VolumeID, key.Protocol)
		}
	})
	metrics.SetVolumeCapacity(stale.VolumeID, stale.Protocol, 1)

	mine := func(protocol string) map[string]string {
		return map[string]string{
			tnsapi.PropertyClusterID:     "cluster-a",
			tnsapi.PropertyProtocol:      protocol,
			tnsapi.PropertyCapacityBytes: "1073741824",
		}
	}
	noProtocol := mine("")
	delete(noProtocol, tnsapi.PropertyProtocol)
	otherCluster := mine(tnsapi.ProtocolISCSI)
	otherCluster[tnsapi.PropertyClusterID] = "cluster-b"
	unclustered := mine(tnsapi.ProtocolISCSI)
	delete(unclustered, tnsapi.PropertyClusterID)
	detached := mine(tnsapi.ProtocolNFS)
	detached[tnsapi.PropertyDetachedSnapshot] = VolumeContextValueTrue
	badCapacity := mine(tnsapi.ProtocolSMB)
	badCapacity[tnsapi.PropertyCapacityBytes] = "lots"

	client := &mockAPIClient{
		findManagedDatasetsFunc: func(_ context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error) {
			if prefix != "" {
				t.Errorf("FindManagedDatasets prefix = %q, want all pools", prefix)
			}
			return []tnsapi.DatasetWithProperties{
				managedDataset("tank/csi/pvc-nfs", mine(tnsapi.ProtocolNFS)),
				managedDataset("tank/csi/pvc-nvme", mine(tnsapi.ProtocolNVMeOF)),
				managedDataset("tank/csi/pvc-no-protocol", noProtocol),
				managedDataset("tank/csi/pvc-other-cluster", otherCluster),
				managedDataset("tank/csi/pvc-unclustered", unclustered),
				managedDataset("tank/csi/snap-detached", detached),
				managedDataset("tank/csi/pvc-bad-capacity", badCapacity),
			}, nil
		},
	}
	s := NewControllerService(client, nil, "cluster-a")

	if err := s.syncVolumeCapacityMetrics(context.Background()); err != nil {
		t.Fatalf("syncVolumeCapacityMetrics() error = %v", err)
	}

	got := metrics.VolumeCapacitySnapshot()
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("missing capacity series %v", key)
		}
	}
	for _, key := range notWant {
		if _, ok := got[key]; ok {
			t.Errorf("unexpected capacity series %v", key)
		}
	}
}

func TestSyncVolumeCapacityMetricsKeepsSeriesOnListError(t *testing.T) {
	existing := metrics.VolumeCapacityKey{VolumeID: "tank/csi/pvc-existing", Protocol: tnsapi.ProtocolNFS}
	t.Cleanup(func() { metrics.DeleteVolumeCapacity(existing.VolumeID, existing.Protocol) })
	metrics.SetVolumeCapacity(existing.VolumeID, existing.Protocol, 1)

	client := &mockAPIClient{
		findManagedDatasetsFunc: func(context.Context, string) ([]tnsapi.DatasetWithProperties, error) {
			return nil, errors.New("connection refused")
		},
	}
	s := NewControllerService(client, nil, "")

	if err := s.syncVolumeCapacityMetrics(context.Background()); err == nil {
		t.Fatal("syncVolumeCapacityMetrics() error = nil, want the listing error")
	}
	if _, ok := metrics.VolumeCapacitySnapshot()[existing]; !ok {
		t.Error("a failed listing removed an existing capacity series")
	}
}
