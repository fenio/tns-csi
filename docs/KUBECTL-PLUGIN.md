# kubectl tns-csi Plugin

A kubectl plugin for managing TrueNAS CSI driver volumes from the command line.

## Installation

### Via Krew (Recommended)

[Krew](https://krew.sigs.k8s.io/) is the plugin manager for kubectl.

```bash
# Install krew if you haven't already
# See: https://krew.sigs.k8s.io/docs/user-guide/setup/install/

# Install the plugin
kubectl krew install tns-csi

# Verify installation
kubectl tns-csi --version
```

### Manual Installation

Download the appropriate binary from [GitHub Releases](https://github.com/fenio/tns-csi/releases):

```bash
# Linux amd64
curl -LO https://github.com/fenio/tns-csi/releases/download/plugin-v0.1.0/kubectl-tns_csi-linux-amd64.tar.gz
tar -xzf kubectl-tns_csi-linux-amd64.tar.gz
mv kubectl-tns_csi-linux-amd64/kubectl-tns_csi /usr/local/bin/

# macOS arm64 (Apple Silicon)
curl -LO https://github.com/fenio/tns-csi/releases/download/plugin-v0.1.0/kubectl-tns_csi-darwin-arm64.tar.gz
tar -xzf kubectl-tns_csi-darwin-arm64.tar.gz
mv kubectl-tns_csi-darwin-arm64/kubectl-tns_csi /usr/local/bin/

# Verify
kubectl tns-csi --version
```

## Configuration

The plugin automatically discovers TrueNAS credentials from the installed driver, so it **works out of the box** on clusters with tns-csi installed.

### Credential Discovery Priority

1. **Explicit flags**: `--url` and `--api-key`
2. **Explicit secret**: `--secret namespace/name`
3. **Auto-discovery**: Searches `kube-system` for driver secrets
4. **Environment variables**: `TRUENAS_URL` and `TRUENAS_API_KEY`

### Examples

```bash
# On a cluster with tns-csi installed - just works!
kubectl tns-csi list

# Explicit credentials via flags
kubectl tns-csi list --url wss://truenas:443/api/current --api-key YOUR-API-KEY

# Using a specific secret
kubectl tns-csi list --secret kube-system/my-truenas-secret

# Via environment variables
export TRUENAS_URL=wss://truenas:443/api/current
export TRUENAS_API_KEY=YOUR-API-KEY
kubectl tns-csi list
```

## Commands

### Overview Commands

#### `summary`
Display a dashboard-style overview of all tns-csi managed resources.

```bash
kubectl tns-csi summary
```

Output:
```
╔════════════════════════════════════════════════════════════════╗
║                    TNS-CSI Summary                             ║
╠════════════════════════════════════════════════════════════════╣
║  VOLUMES                                                       ║
║    Total: 12    NFS: 8    NVMe-oF: 4    Clones: 2             ║
╠────────────────────────────────────────────────────────────────╣
║  SNAPSHOTS                                                     ║
║    Total: 5     Attached: 3    Detached: 2                    ║
╠────────────────────────────────────────────────────────────────╣
║  CAPACITY                                                      ║
║    Provisioned: 500 GiB    Used: 125 GiB    (25.0%)           ║
╠────────────────────────────────────────────────────────────────╣
║  HEALTH                                                        ║
║    ✓ Healthy: 12                                              ║
╚════════════════════════════════════════════════════════════════╝
```

### Listing Commands

#### `list`
List all tns-csi managed volumes with their properties.

```bash
kubectl tns-csi list
kubectl tns-csi list -o json    # JSON output
kubectl tns-csi list -o yaml    # YAML output
```

Shows: Dataset, Volume ID, Protocol, Capacity, Adoptable status, Clone source

#### `list-snapshots`
List all snapshots (both attached ZFS snapshots and detached snapshot datasets).

```bash
kubectl tns-csi list-snapshots
```

Shows: Snapshot name, Source volume, Protocol, Type (attached/detached)

#### `list-orphaned`
Find volumes that exist on TrueNAS but have no matching PVC in Kubernetes.

```bash
kubectl tns-csi list-orphaned
```

Useful for disaster recovery and cleanup scenarios.

#### `list-clones`
List all cloned volumes with their dependency relationships.

```bash
kubectl tns-csi list-clones
kubectl tns-csi list-clones -o yaml
```

Shows clone mode and helps understand what can and cannot be deleted:
- **cow** (Copy-on-Write): Clone depends on snapshot. Snapshot CANNOT be deleted.
- **promoted**: Snapshot depends on clone. Snapshot CAN be deleted.
- **detached**: No dependency. Both can be deleted independently.

#### `list-unmanaged`
List volumes not managed by tns-csi (useful for discovering volumes to import).

```bash
kubectl tns-csi list-unmanaged --pool storage
kubectl tns-csi list-unmanaged --parent storage/k8s
kubectl tns-csi list-unmanaged --pool storage --all    # Include system datasets
kubectl tns-csi list-unmanaged --pool storage -o json
```

Shows:
- Dataset path and name
- Type (filesystem or zvol)
- Detected protocol (NFS if share exists)
- Size information
- Any existing management markers (e.g., democratic-csi)

| Flag | Description |
|------|-------------|
| `--pool` | ZFS pool to search in |
| `--parent` | Parent dataset path to search under |
| `--all` | Show all datasets including system datasets |

### Diagnostic Commands

#### `describe`
Show detailed information about a specific volume.

```bash
kubectl tns-csi describe <volume-id>
kubectl tns-csi describe tank/csi/pvc-xxx    # By dataset path
```

Shows: Volume details, capacity, NFS share or NVMe subsystem info, all ZFS properties

#### `health`
Check the health of all managed volumes.

```bash
kubectl tns-csi health           # Show only issues
kubectl tns-csi health --all     # Show all volumes
```

Checks:
- Dataset exists on TrueNAS
- NFS shares are present and enabled
- NVMe-oF subsystems are present and enabled

#### `troubleshoot`
Comprehensive diagnostics for a PVC that isn't working.

```bash
kubectl tns-csi troubleshoot <pvc-name> -n <namespace>
kubectl tns-csi troubleshoot my-pvc -n default --logs
```

Checks:
- PVC exists and is bound
- PV exists and has valid handle
- TrueNAS connection works
- Dataset exists
- NFS share / NVMe subsystem is healthy
- Recent events and controller logs

#### `connectivity`
Test connection to TrueNAS.

```bash
kubectl tns-csi connectivity
```

### Maintenance Commands

#### `cleanup`
Delete orphaned volumes from TrueNAS.

```bash
kubectl tns-csi cleanup                    # Dry-run (preview only)
kubectl tns-csi cleanup --execute          # Actually delete (with confirmation)
kubectl tns-csi cleanup --execute --yes    # Delete without confirmation
kubectl tns-csi cleanup --execute --force  # Delete even non-adoptable volumes
```

Safety features:
- Dry-run by default
- Requires confirmation before deletion
- Only deletes volumes marked as adoptable (unless `--force`)
- Properly cleans up NFS shares and NVMe subsystems

#### `mark-adoptable`
Mark volumes as adoptable for disaster recovery or migration.

```bash
kubectl tns-csi mark-adoptable <volume-id>           # Mark single volume
kubectl tns-csi mark-adoptable --all                 # Mark all volumes
kubectl tns-csi mark-adoptable --unmark <volume-id>  # Remove flag
kubectl tns-csi mark-adoptable --unmark --all        # Remove from all
```

### Adoption Commands

**For complete adoption workflows including Kubernetes-side steps, see [ADOPTION.md](ADOPTION.md).**

The commands below handle the TrueNAS-side operations. Full adoption also requires Kubernetes-side steps (scaling down workloads, managing PVCs, etc.) which are documented in the adoption guide.

#### `import`
Import an existing dataset into tns-csi management.

```bash
# Import an NFS dataset (auto-detect existing share)
kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs

# Import and create NFS share if missing
kubectl tns-csi import storage/data/myvolume --protocol nfs --create-share

# Import with custom volume ID
kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs --volume-id my-volume

# Dry run to see what would happen
kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs --dry-run
```

Useful for:
- Migrating volumes from democratic-csi
- Adopting manually created datasets
- Taking over volumes from other CSI drivers

| Flag | Description |
|------|-------------|
| `--protocol` | Protocol: nfs or nvmeof (required) |
| `--volume-id` | Custom volume ID (defaults to dataset name) |
| `--create-share` | Create NFS share if it doesn't exist |
| `--storage-class` | StorageClass to associate with the volume |
| `--dry-run` | Show what would be done without making changes |

After importing, use `kubectl tns-csi adopt <dataset>` to generate PV/PVC manifests.

#### `adopt`
Generate a PersistentVolume manifest to adopt an existing volume.

```bash
kubectl tns-csi adopt <dataset-path>
kubectl tns-csi adopt tank/csi/my-volume -o yaml > pv.yaml
kubectl apply -f pv.yaml
```

#### `status`
Show the current status of a volume from TrueNAS.

```bash
kubectl tns-csi status <pvc-name>
```

### Web Dashboard

#### `serve`
Start a web-based dashboard for viewing tns-csi resources in your browser.

```bash
# Start dashboard on default port 8080
kubectl tns-csi serve

# Start on custom port
kubectl tns-csi serve --port 9090

# With pool for unmanaged volume discovery
kubectl tns-csi serve --pool storage
```

The dashboard provides:
- **Summary cards** - Total volumes, snapshots, clones, and capacity
- **Volumes tab** - All managed volumes with protocol, capacity, and adoptable status
- **Snapshots tab** - All snapshots with source volume and type (attached/detached)
- **Clones tab** - Cloned volumes with dependency information
- **Unmanaged tab** - Volumes not managed by tns-csi (requires `--pool` flag)

Features:
- Dark theme UI
- Real-time refresh via htmx
- Auto-detects democratic-csi managed volumes
- Shows container datasets vs actual volumes

| Flag | Description |
|------|-------------|
| `--port` | Port to listen on (default: 8080) |
| `--pool` | ZFS pool to search for unmanaged volumes |

Access the dashboard at `http://localhost:8080` after starting.

## Output Formats

All commands support multiple output formats:

```bash
kubectl tns-csi list              # Table (default)
kubectl tns-csi list -o table     # Table (explicit)
kubectl tns-csi list -o json      # JSON
kubectl tns-csi list -o yaml      # YAML
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--url` | TrueNAS WebSocket URL (wss://host/api/current) |
| `--api-key` | TrueNAS API key |
| `--secret` | Kubernetes secret with credentials (namespace/name) |
| `-o, --output` | Output format: table, json, yaml |
| `--insecure-skip-tls-verify` | Skip TLS verification (default: true) |

## Use Cases

### Disaster Recovery

1. Prepare volumes for potential cluster loss:
   ```bash
   kubectl tns-csi mark-adoptable --all
   ```

2. After cluster recreation, find orphaned volumes:
   ```bash
   kubectl tns-csi list-orphaned
   ```

3. Adopt volumes into the new cluster:
   ```bash
   kubectl tns-csi adopt tank/csi/pvc-xxx > pv.yaml
   kubectl apply -f pv.yaml
   ```

### Routine Maintenance

1. Check overall health:
   ```bash
   kubectl tns-csi summary
   kubectl tns-csi health
   ```

2. Clean up orphaned volumes:
   ```bash
   kubectl tns-csi cleanup              # Preview
   kubectl tns-csi cleanup --execute    # Clean up
   ```

### Troubleshooting

1. PVC stuck in Pending:
   ```bash
   kubectl tns-csi troubleshoot my-pvc -n default --logs
   ```

2. Check specific volume:
   ```bash
   kubectl tns-csi describe pvc-xxx
   ```

## Building from Source

```bash
# Clone the repository
git clone https://github.com/fenio/tns-csi.git
cd tns-csi

# Build the plugin
go build -o kubectl-tns_csi ./cmd/kubectl-tns-csi

# Install
mv kubectl-tns_csi /usr/local/bin/
```
