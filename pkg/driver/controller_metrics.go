package driver

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"k8s.io/klog/v2"
)

const (
	// volumeCapacitySyncInterval is how often the controller rebuilds
	// tns_csi_volume_capacity_bytes from TrueNAS.
	volumeCapacitySyncInterval = 5 * time.Minute
	volumeCapacitySyncTimeout  = time.Minute
)

// runVolumeCapacitySync rebuilds the volume capacity metric from TrueNAS now and then
// every interval until ctx is done. CSI calls only update the metric for the volume
// they touch, so without this it is empty after a controller restart (issue #298)
// and keeps volumes that were deleted outside Kubernetes.
func (s *ControllerService) runVolumeCapacitySync(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		syncCtx, cancel := context.WithTimeout(ctx, volumeCapacitySyncTimeout)
		if err := s.syncVolumeCapacityMetrics(syncCtx); err != nil {
			klog.Warningf("Failed to sync volume capacity metrics: %v", err)
		}
		cancel()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// syncVolumeCapacityMetrics sets tns_csi_volume_capacity_bytes for every volume this
// cluster owns on TrueNAS and removes series for volumes that no longer exist.
func (s *ControllerService) syncVolumeCapacityMetrics(ctx context.Context) error {
	before := metrics.VolumeCapacitySnapshot()

	datasets, err := s.apiClient.FindManagedDatasets(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to find managed datasets: %w", err)
	}

	current := make(map[metrics.VolumeCapacityKey]int64, len(datasets))
	for i := range datasets {
		ds := &datasets[i]
		props := ds.UserProperties
		if isSnapshotDataset(props) {
			continue
		}
		// Volumes of other clusters sharing this TrueNAS belong to their own controllers.
		if props[tnsapi.PropertyClusterID].Value != s.clusterID {
			continue
		}
		capacity, parseErr := strconv.ParseInt(props[tnsapi.PropertyCapacityBytes].Value, 10, 64)
		if parseErr != nil {
			klog.V(4).Infof("Skipping capacity metric for %s: invalid %s: %v", ds.ID, tnsapi.PropertyCapacityBytes, parseErr)
			continue
		}
		protocol := props[tnsapi.PropertyProtocol].Value
		if protocol == "" {
			protocol = metrics.ProtocolUnknown
		}
		current[metrics.VolumeCapacityKey{VolumeID: ds.ID, Protocol: protocol}] = capacity
	}

	metrics.SyncVolumeCapacities(before, current)
	klog.V(4).Infof("Synced capacity metrics for %d volumes", len(current))
	return nil
}
