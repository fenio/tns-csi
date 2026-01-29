# Prometheus Metrics

The TNS CSI Driver exposes Prometheus metrics on the controller pod to provide observability into volume operations, WebSocket connection health, and CSI operations.

## Metrics Endpoint

By default, metrics are exposed on port `8080` at the `/metrics` endpoint. The metrics endpoint is only available on the controller pod.

## Available Metrics

### CSI Operation Metrics

These metrics track all CSI RPC operations:

- **`tns_csi_operations_total`** (counter)
  - Total number of CSI operations
  - Labels: `method` (CSI method name, e.g., CreateVolume, DeleteVolume), `grpc_status_code`

- **`tns_csi_operations_duration_seconds`** (histogram)
  - Duration of CSI operations in seconds
  - Labels: `method`, `grpc_status_code`
  - Buckets: 0.1s, 0.5s, 1s, 2.5s, 5s, 10s, 30s, 60s

### Volume Operation Metrics

Protocol-specific volume operations (NFS, NVMe-oF, and iSCSI):

- **`tns_volume_operations_total`** (counter)
  - Total number of volume operations
  - Labels: `protocol` (nfs, nvmeof, or iscsi), `operation` (create, delete, expand), `status` (success or error)

- **`tns_volume_operations_duration_seconds`** (histogram)
  - Duration of volume operations in seconds
  - Labels: `protocol`, `operation`, `status`
  - Buckets: 0.5s, 1s, 2s, 5s, 10s, 30s, 60s, 120s

- **`tns_volume_capacity_bytes`** (gauge)
  - Capacity of provisioned volumes in bytes
  - Labels: `volume_id`, `protocol`

### WebSocket Connection Metrics

Metrics for the TrueNAS API WebSocket connection:

- **`tns_websocket_connected`** (gauge)
  - WebSocket connection status (1 = connected, 0 = disconnected)

- **`tns_websocket_reconnects_total`** (counter)
  - Total number of WebSocket reconnection attempts

- **`tns_websocket_messages_total`** (counter)
  - Total number of WebSocket messages
  - Labels: `direction` (sent or received)

- **`tns_websocket_message_duration_seconds`** (histogram)
  - Duration of WebSocket RPC calls in seconds
  - Labels: `method` (TrueNAS API method name)
  - Buckets: 0.1s, 0.25s, 0.5s, 1s, 2s, 5s, 10s, 30s

- **`tns_websocket_connection_duration_seconds`** (gauge)
  - Current WebSocket connection duration in seconds (updated every 20s)

## Configuration

### Enabling Metrics

Metrics are enabled by default. To disable them:

```yaml
controller:
  metrics:
    enabled: false
```

### Changing Metrics Port

To use a different port:

```yaml
controller:
  metrics:
    enabled: true
    port: 9090
```

### Creating Metrics Service

A Kubernetes Service is created by default to expose the metrics endpoint:

```yaml
controller:
  metrics:
    enabled: true
    service:
      enabled: true
      type: ClusterIP
      port: 8080
```

### Prometheus Operator Integration

To enable automatic scraping with Prometheus Operator, enable the ServiceMonitor:

```yaml
controller:
  metrics:
    enabled: true
    serviceMonitor:
      enabled: true
      # Add labels that match your Prometheus serviceMonitorSelector
      labels:
        release: prometheus
      interval: 30s
      scrapeTimeout: 10s
```

## Prometheus Configuration

If you're using Prometheus without the Operator, add a scrape config:

```yaml
scrape_configs:
  - job_name: 'tns-csi-driver'
    kubernetes_sd_configs:
      - role: service
        namespaces:
          names:
            - kube-system  # or your CSI driver namespace
    relabel_configs:
      - source_labels: [__meta_kubernetes_service_label_app_kubernetes_io_name]
        action: keep
        regex: tns-csi-driver
      - source_labels: [__meta_kubernetes_service_label_app_kubernetes_io_component]
        action: keep
        regex: controller
```

## Example Queries

### Volume Operations

Total volume operations by protocol:
```promql
sum by (protocol, operation) (rate(tns_volume_operations_total[5m]))
```

Volume operation error rate:
```promql
sum by (protocol, operation) (rate(tns_volume_operations_total{status="error"}[5m])) 
/ 
sum by (protocol, operation) (rate(tns_volume_operations_total[5m]))
```

95th percentile volume operation latency:
```promql
histogram_quantile(0.95, rate(tns_volume_operations_duration_seconds_bucket[5m]))
```

### WebSocket Health

WebSocket connection status:
```promql
tns_websocket_connected
```

WebSocket reconnection rate:
```promql
rate(tns_websocket_reconnects_total[5m])
```

Average WebSocket message duration by method:
```promql
rate(tns_websocket_message_duration_seconds_sum[5m]) 
/ 
rate(tns_websocket_message_duration_seconds_count[5m])
```

### CSI Operations

CSI operation rate by method:
```promql
sum by (method) (rate(tns_csi_operations_total[5m]))
```

CSI operation error rate:
```promql
sum by (method) (rate(tns_csi_operations_total{grpc_status_code!="OK"}[5m])) 
/ 
sum by (method) (rate(tns_csi_operations_total[5m]))
```

95th percentile CSI operation latency:
```promql
histogram_quantile(0.95, 
  sum by (method, le) (rate(tns_csi_operations_duration_seconds_bucket[5m]))
)
```

## Grafana Dashboard

Example Grafana panels:

### Volume Operations Panel
- Query: `sum by (protocol) (rate(tns_volume_operations_total[5m]))`
- Visualization: Time series
- Legend: `{{protocol}}`

### WebSocket Connection Status Panel
- Query: `tns_websocket_connected`
- Visualization: Stat
- Thresholds: Red (0), Green (1)

### Operation Latency Panel
- Query: `histogram_quantile(0.95, rate(tns_volume_operations_duration_seconds_bucket[5m]))`
- Visualization: Time series
- Unit: seconds (s)

## Troubleshooting

### Metrics endpoint not accessible

1. Check if metrics are enabled:
   ```bash
   kubectl get svc -n kube-system | grep tns-csi-driver-metrics
   ```

2. Check controller pod logs:
   ```bash
   kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin
   ```

3. Port-forward to test locally:
   ```bash
   kubectl port-forward -n kube-system svc/tns-csi-driver-metrics 8080:8080
   curl http://localhost:8080/metrics
   ```

### ServiceMonitor not being scraped

1. Verify ServiceMonitor labels match Prometheus selector:
   ```bash
   kubectl get servicemonitor -n kube-system tns-csi-driver -o yaml
   ```

2. Check Prometheus serviceMonitorSelector:
   ```bash
   kubectl get prometheus -A -o yaml | grep -A 5 serviceMonitorSelector
   ```

3. Check Prometheus logs for scrape errors:
   ```bash
   kubectl logs -n monitoring prometheus-xxx
   ```

## Development Notes

Metrics are collected in:
- `pkg/metrics/metrics.go` - Metric definitions and registration
- `pkg/driver/driver.go` - CSI operation metrics via gRPC interceptor
- `pkg/tnsapi/client.go` - WebSocket connection metrics
- `pkg/driver/controller_nfs.go`, `controller_nvmeof.go`, and `controller_iscsi.go` - Protocol-specific volume operation metrics
