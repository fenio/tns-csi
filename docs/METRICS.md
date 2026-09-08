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

Protocol-specific volume operations (NFS, NVMe-oF, iSCSI, and SMB):

- **`tns_volume_operations_total`** (counter)
  - Total number of volume operations
  - Labels: `protocol` (nfs, nvmeof, iscsi, or smb), `operation` (create, delete, expand), `status` (success or error)

- **`tns_volume_operations_duration_seconds`** (histogram)
  - Duration of volume operations in seconds
  - Labels: `protocol`, `operation`, `status`
  - Buckets: 0.5s, 1s, 2s, 5s, 10s, 30s, 60s, 120s

- **`tns_volume_capacity_bytes`** (gauge)
  - Capacity of provisioned volumes in bytes
  - Labels: `volume_id`, `protocol`

### NVMe-oF Connect Concurrency Metrics

- **`tns_csi_nvme_connect_concurrent`** (gauge)
  - Number of NVMe-oF connect operations currently in progress

- **`tns_csi_nvme_connect_waiting`** (gauge)
  - Number of NVMe-oF connect operations waiting for the semaphore
  - Non-zero values indicate the concurrency limit is actively throttling connections

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
            - tns-csi  # or your CSI driver namespace
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

The Helm chart includes a pre-built Grafana dashboard (`tns-csi-overview.json`) that provides a comprehensive view of driver operations.

### Enabling the Grafana Dashboard

Enable automatic provisioning via Helm values:

```yaml
grafana:
  dashboards:
    enabled: true
    labels:
      grafana_dashboard: "1"    # Must match your Grafana sidecar label selector
    annotations: {}
```

This creates a ConfigMap (`tns-csi-driver-grafana-dashboard`) with the `grafana_dashboard: "1"` label. If your Grafana deployment uses a sidecar (standard with kube-prometheus-stack), the dashboard is auto-discovered and loaded.

### Dashboard Panels

The dashboard includes:

- **WebSocket Connection** — connection status, duration, and reconnect count
- **Operations Overview** — total operations by protocol (NFS, NVMe-oF, iSCSI, SMB) with success/error breakdown
- **Operations by Type** — create, delete, expand counts per protocol
- **Message Throughput** — WebSocket messages sent/received over time
- **Per-Protocol Breakdown** — dedicated panels for NFS, NVMe-oF, iSCSI, and SMB operations

### Manual Import

If you don't use Grafana sidecar discovery, import the dashboard JSON manually:

1. Copy `charts/tns-csi-driver/dashboards/tns-csi-overview.json`
2. In Grafana: **Dashboards** > **Import** > paste the JSON
3. Select your Prometheus data source

## In-Cluster Web Dashboard

The controller pod can serve a live web dashboard showing volume health, Kubernetes binding, and protocol-specific details.

### Enabling the Dashboard

```yaml
controller:
  dashboard:
    enabled: true
    port: 9090
    service:
      enabled: true
      type: ClusterIP
      port: 9090
    ingress:
      enabled: false    # Optional: expose via Ingress
```

### Accessing the Dashboard

```bash
# Port-forward to the dashboard service
kubectl port-forward -n tns-csi svc/tns-csi-driver-dashboard 9090:9090

# Open http://localhost:9090/dashboard/
```

### Dashboard Features

The in-cluster dashboard provides:

- **Volume inventory** — all managed volumes with protocol, capacity, and health status
- **Volume health checks** — verifies dataset exists, NFS shares/SMB shares/NVMe-oF subsystems/iSCSI targets are valid
- **Kubernetes binding** — shows PV/PVC names, namespaces, and attached pods
- **Snapshot and clone tracking** — lists all snapshots and clones with source volumes
- **Unmanaged volume discovery** — finds non-CSI volumes on the same pool (requires `--dashboard-pool`)
- **Metrics summary** — parsed Prometheus metrics (operations, WebSocket health)

### API Endpoints

The dashboard exposes JSON API endpoints at `/dashboard/api/`:

| Endpoint | Description |
|----------|-------------|
| `GET /dashboard/api/volumes` | List all managed volumes |
| `GET /dashboard/api/volumes/{id}` | Volume details with health check |
| `GET /dashboard/api/snapshots` | List all snapshots |
| `GET /dashboard/api/clones` | List all clones |
| `GET /dashboard/api/summary` | Summary statistics |
| `GET /dashboard/api/unmanaged` | Unmanaged volumes (needs `--dashboard-pool`) |
| `GET /dashboard/api/metrics` | Parsed Prometheus metrics |
| `GET /dashboard/api/metrics/raw` | Raw Prometheus text format |

### kubectl Plugin Dashboard

The kubectl plugin includes a local dashboard that connects directly to TrueNAS:

```bash
# Start dashboard (auto-opens browser at http://localhost:2137)
kubectl tns-csi dashboard

# Custom port, without auto-open
kubectl tns-csi dashboard --port 9090 --open=false

# With pool for unmanaged volume discovery
kubectl tns-csi dashboard --pool storage
```

The plugin auto-discovers TrueNAS credentials from the installed driver's Secret. Both dashboards (in-cluster and kubectl plugin) share the same UI — the difference is where they run: in-cluster runs inside the controller pod, while the plugin runs locally on your machine.

## Troubleshooting

### Metrics endpoint not accessible

1. Check if metrics are enabled:
   ```bash
   kubectl get svc -n tns-csi | grep tns-csi-driver-metrics
   ```

2. Check controller pod logs:
   ```bash
   kubectl logs -n tns-csi -l app.kubernetes.io/component=controller -c tns-csi-plugin
   ```

3. Port-forward to test locally:
   ```bash
   kubectl port-forward -n tns-csi svc/tns-csi-driver-metrics 8080:8080
   curl http://localhost:8080/metrics
   ```

### ServiceMonitor not being scraped

1. Verify ServiceMonitor labels match Prometheus selector:
   ```bash
   kubectl get servicemonitor -n tns-csi tns-csi-driver -o yaml
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
- `pkg/driver/controller_nfs.go`, `controller_nvmeof.go`, `controller_iscsi.go`, and `controller_smb.go` - Protocol-specific volume operation metrics
