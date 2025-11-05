// Package metrics provides Prometheus metrics for the TNS CSI driver.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	namespace = "tns_csi"
)

// Operation types for CSI operations
const (
	// Controller operations
	OpCreateVolume              = "CreateVolume"
	OpDeleteVolume              = "DeleteVolume"
	OpControllerPublish         = "ControllerPublishVolume"
	OpControllerUnpublish       = "ControllerUnpublishVolume"
	OpValidateCapabilities      = "ValidateVolumeCapabilities"
	OpListVolumes               = "ListVolumes"
	OpGetCapacity               = "GetCapacity"
	OpControllerGetCapabilities = "ControllerGetCapabilities"
	OpCreateSnapshot            = "CreateSnapshot"
	OpDeleteSnapshot            = "DeleteSnapshot"
	OpListSnapshots             = "ListSnapshots"
	OpExpandVolume              = "ControllerExpandVolume"

	// Node operations
	OpNodeStage           = "NodeStageVolume"
	OpNodeUnstage         = "NodeUnstageVolume"
	OpNodePublish         = "NodePublishVolume"
	OpNodeUnpublish       = "NodeUnpublishVolume"
	OpNodeGetCapabilities = "NodeGetCapabilities"
	OpNodeGetInfo         = "NodeGetInfo"
	OpNodeExpandVolume    = "NodeExpandVolume"

	// Identity operations
	OpGetPluginInfo         = "GetPluginInfo"
	OpGetPluginCapabilities = "GetPluginCapabilities"
	OpProbe                 = "Probe"
)

// Protocol types
const (
	ProtocolNFS     = "nfs"
	ProtocolNVMeOF  = "nvmeof"
	ProtocolUnknown = "unknown"
)

var (
	// CSI operation metrics
	csiOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "operations_total",
			Help:      "Total number of CSI operations by operation type and status",
		},
		[]string{"operation", "status"},
	)

	csiOperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "operation_duration_seconds",
			Help:      "Duration of CSI operations in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~16s
		},
		[]string{"operation"},
	)

	// Volume operation metrics with protocol labels
	volumeOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "volume_operations_total",
			Help:      "Total number of volume operations by protocol, operation type and status",
		},
		[]string{"protocol", "operation", "status"},
	)

	volumeOperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "volume_operation_duration_seconds",
			Help:      "Duration of volume operations in seconds by protocol",
			Buckets:   prometheus.ExponentialBuckets(0.1, 2, 12), // 100ms to ~400s
		},
		[]string{"protocol", "operation"},
	)

	// WebSocket connection metrics
	wsConnectionStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "websocket_connection_status",
			Help:      "WebSocket connection status (1 = connected, 0 = disconnected)",
		},
	)

	wsReconnectionsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "websocket_reconnections_total",
			Help:      "Total number of WebSocket reconnection attempts",
		},
	)

	wsMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "websocket_messages_total",
			Help:      "Total number of WebSocket messages by direction",
		},
		[]string{"direction"}, // sent, received
	)

	wsMessageDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "websocket_message_duration_seconds",
			Help:      "Duration of WebSocket API calls (request to response)",
			Buckets:   prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms to ~40s
		},
		[]string{"method"},
	)

	wsConnectionDuration = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "websocket_connection_duration_seconds",
			Help:      "Duration of current WebSocket connection in seconds",
		},
	)

	// Volume capacity metrics
	volumeCapacityBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "volume_capacity_bytes",
			Help:      "Volume capacity in bytes",
		},
		[]string{"volume_id", "protocol"},
	)
)

// RecordCSIOperation records the outcome of a CSI operation.
func RecordCSIOperation(operation, status string, duration time.Duration) {
	csiOperationsTotal.WithLabelValues(operation, status).Inc()
	csiOperationDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordVolumeOperation records the outcome of a volume operation with protocol.
func RecordVolumeOperation(protocol, operation, status string, duration time.Duration) {
	volumeOperationsTotal.WithLabelValues(protocol, operation, status).Inc()
	volumeOperationDuration.WithLabelValues(protocol, operation).Observe(duration.Seconds())
}

// SetWSConnectionStatus sets the WebSocket connection status.
func SetWSConnectionStatus(connected bool) {
	if connected {
		wsConnectionStatus.Set(1)
	} else {
		wsConnectionStatus.Set(0)
	}
}

// RecordWSReconnection increments the WebSocket reconnection counter.
func RecordWSReconnection() {
	wsReconnectionsTotal.Inc()
}

// RecordWSMessage records a WebSocket message.
func RecordWSMessage(direction string) {
	wsMessagesTotal.WithLabelValues(direction).Inc()
}

// RecordWSMessageDuration records the duration of a WebSocket API call.
func RecordWSMessageDuration(method string, duration time.Duration) {
	wsMessageDuration.WithLabelValues(method).Observe(duration.Seconds())
}

// SetWSConnectionDuration sets the current WebSocket connection duration.
func SetWSConnectionDuration(duration time.Duration) {
	wsConnectionDuration.Set(duration.Seconds())
}

// SetVolumeCapacity sets the capacity of a volume.
func SetVolumeCapacity(volumeID, protocol string, bytes int64) {
	volumeCapacityBytes.WithLabelValues(volumeID, protocol).Set(float64(bytes))
}

// DeleteVolumeCapacity removes the capacity metric for a deleted volume.
func DeleteVolumeCapacity(volumeID, protocol string) {
	volumeCapacityBytes.DeleteLabelValues(volumeID, protocol)
}

// OperationTimer helps time operations and record metrics automatically.
type OperationTimer struct {
	start     time.Time
	operation string
	protocol  string // empty for non-volume operations
}

// NewOperationTimer creates a new timer for a CSI operation.
func NewOperationTimer(operation string) *OperationTimer {
	return &OperationTimer{
		start:     time.Now(),
		operation: operation,
	}
}

// NewVolumeOperationTimer creates a new timer for a volume operation with protocol.
func NewVolumeOperationTimer(protocol, operation string) *OperationTimer {
	return &OperationTimer{
		start:     time.Now(),
		operation: operation,
		protocol:  protocol,
	}
}

// ObserveSuccess records a successful operation.
func (t *OperationTimer) ObserveSuccess() {
	duration := time.Since(t.start)
	if t.protocol != "" {
		RecordVolumeOperation(t.protocol, t.operation, "success", duration)
	}
	RecordCSIOperation(t.operation, "success", duration)
}

// ObserveError records a failed operation.
func (t *OperationTimer) ObserveError() {
	duration := time.Since(t.start)
	if t.protocol != "" {
		RecordVolumeOperation(t.protocol, t.operation, "error", duration)
	}
	RecordCSIOperation(t.operation, "error", duration)
}

// WSMessageTimer helps time WebSocket API calls.
type WSMessageTimer struct {
	start  time.Time
	method string
}

// NewWSMessageTimer creates a new timer for a WebSocket API call.
func NewWSMessageTimer(method string) *WSMessageTimer {
	return &WSMessageTimer{
		start:  time.Now(),
		method: method,
	}
}

// Observe records the duration of the WebSocket API call.
func (t *WSMessageTimer) Observe() {
	RecordWSMessageDuration(t.method, time.Since(t.start))
}
