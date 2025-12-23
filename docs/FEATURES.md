# TNS CSI Driver - Feature Support Documentation

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This document provides a comprehensive overview of currently implemented and tested features in the TNS CSI Driver.

## Overview

The TNS CSI Driver is a Kubernetes Container Storage Interface (CSI) driver that enables dynamic provisioning and management of persistent storage volumes on TrueNAS systems. This driver is in active development with core features implemented and undergoing testing.

## Supported Storage Protocols

### NFS (Network File System)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteMany (RWX), ReadWriteOnce (RWO)
- **Use Case**: Shared filesystem storage, multi-pod access
- **Mount Protocol**: NFSv4.2 with nolock option
- **TrueNAS Requirements**: 
  - TrueNAS Scale 25.10+
  - NFS service enabled
  - Accessible NFS ports (111, 2049)

### NVMe-oF (NVMe over Fabrics - TCP)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteOnce (RWO)
- **Use Case**: High-performance block storage, low-latency workloads
- **Transport**: TCP (nvme-tcp)
- **TrueNAS Requirements**:
  - TrueNAS Scale 25.10+ (NVMe-oF feature introduced in this version)
  - Static IP address configured (DHCP not supported)
  - Pre-configured NVMe-oF subsystem with TCP port (default: 4420)
  - At least one initial namespace in subsystem
- **Architecture**: Shared subsystem model (1 subsystem → many namespaces)

### Why These Protocols?

**NVMe-oF over iSCSI**: NVMe-oF provides superior performance with:
- Lower latency
- Higher IOPS
- Better utilization of modern NVMe SSDs
- Native NVMe command set over fabric

**No SMB/CIFS Support**: Low priority due to Linux-native protocol focus. Consider Democratic-CSI driver if Windows file sharing is required.

## Core CSI Features

### Volume Lifecycle Management

#### Dynamic Provisioning
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF
- **Description**: Automatic creation of storage volumes when PVCs are created
- **Implementation**:
  - NFS: Creates ZFS dataset and NFS share automatically
  - NVMe-oF: Creates ZVOL, namespace, and configures NVMe-oF target
- **Parameters**:
  - `protocol`: nfs or nvmeof
  - `pool`: ZFS pool name
  - `server`: TrueNAS IP/hostname
  - `subsystemNQN`: (NVMe-oF only) Pre-configured subsystem NQN

#### Volume Deletion
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF
- **Description**: Automatic cleanup when PVCs with reclaimPolicy: Delete are removed
- **Implementation**:
  - NFS: Removes NFS share and deletes ZFS dataset
  - NVMe-oF: Removes namespace from subsystem and deletes ZVOL
  - Idempotent operations (safe to retry)
  - Supports `deleteStrategy` parameter for volume retention (see below)

#### Delete Strategy (Volume Retention)
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF
- **Description**: Control whether volumes are actually deleted or retained when a PVC is deleted
- **Parameter**: `deleteStrategy` in StorageClass parameters
- **Values**:
  - `delete` (default): Volume is deleted when PVC is deleted
  - `retain`: Volume is kept on TrueNAS when PVC is deleted (useful for data protection)
- **Use Case**: Protect important data from accidental deletion while still using `reclaimPolicy: Delete`

**Example StorageClass with Delete Strategy:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-retained
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  deleteStrategy: "retain"  # Volumes kept on TrueNAS when PVC deleted
allowVolumeExpansion: true
reclaimPolicy: Delete
```

#### Volume Attachment/Detachment
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF
- **Description**: Attach volumes to nodes and detach when no longer needed
- **Implementation**:
  - NFS: Handled by NFSv4 protocol
  - NVMe-oF: Uses nvme-cli for discovery, connect, and disconnect operations

#### Volume Mounting/Unmounting
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF
- **Description**: Mount volumes into pod containers at specified paths
- **Implementation**:
  - NFS: Standard NFSv4.2 mount with optimized options
  - NVMe-oF: Block device formatting (ext4/xfs) and filesystem mount
  - Proper cleanup on unmount

### Configurable Mount Options
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF
- **Description**: Customize mount options via StorageClass `mountOptions` field
- **Behavior**: User-specified options are merged with sensible defaults, with user options taking precedence for conflicting keys

**Default Mount Options:**
| Protocol | Platform | Defaults |
|----------|----------|----------|
| NFS | Linux | `vers=4.2`, `nolock` |
| NFS | macOS | `vers=4`, `nolock` |
| NVMe-oF | Linux | `noatime` |

**Example StorageClass with Custom Mount Options:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-custom
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
mountOptions:
  - hard
  - nointr
  - rsize=1048576
  - wsize=1048576
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**NVMe-oF Mount Options Example:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nvmeof-custom
provisioner: tns.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: truenas.local
  subsystemNQN: nqn.2025-01.com.truenas:csi
mountOptions:
  - discard
  - data=ordered
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### Volume Expansion
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF
- **Description**: Dynamically resize volumes without downtime
- **Requirements**: StorageClass must have `allowVolumeExpansion: true` (enabled by default in Helm chart)
- **Limitations**:
  - Only expansion supported (shrinking not possible)
  - Volume must not be in use during expansion for some operations
- **Implementation**:
  - NFS: Expands ZFS dataset quota
  - NVMe-oF: Expands ZVOL size and resizes filesystem

**Example:**
```bash
kubectl patch pvc my-pvc -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

### Volume Snapshots
- **Status**: ✅ Implemented, testing in progress
- **Protocols**: NFS, NVMe-oF
- **Description**: Create point-in-time copies of volumes using ZFS snapshots
- **Features**:
  - Near-instant snapshot creation
  - Space-efficient (copy-on-write)
  - Snapshot deletion with proper cleanup
  - List snapshots
- **Requirements**:
  - Kubernetes Snapshot CRDs (v1 API)
  - External snapshot controller
  - CSI snapshotter sidecar (included in Helm chart)

**Key Operations:**
- Create snapshot: ZFS snapshot created instantly
- Delete snapshot: Snapshot removed from ZFS
- Idempotent operations

### Volume Cloning (Restore from Snapshot)
- **Status**: ✅ Implemented, testing in progress
- **Protocols**: NFS, NVMe-oF
- **Description**: Create new volumes from existing snapshots
- **Features**:
  - Instant clone creation via ZFS clone
  - Space-efficient (shares blocks with snapshot until modified)
  - Full read/write access to cloned volume
  - **Detached clones** (promoted) for independent volumes (see below)
- **Limitations**:
  - Cannot clone across protocols (NFS snapshot → NFS volume only)
  - Must restore to same or larger size
  - Same ZFS pool required

### Detached Clones (Independent Clone Restoration)
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF
- **Description**: Create clones that are independent from the source snapshot
- **Features**:
  - Clone is promoted immediately after creation
  - No dependency on parent snapshot
  - Source snapshot can be deleted without affecting the clone
  - Useful for snapshot rotation and cleanup
- **Parameter**: `detached: "true"` in StorageClass parameters
- **Use Cases**:
  - Snapshot rotation policies where old snapshots need to be cleaned up
  - Creating fully independent copies of data
  - Avoiding clone dependency issues

**Example StorageClass with Detached Clones:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-detached
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  detached: "true"  # Clones will be promoted to break parent dependency
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### Detached Snapshots (Survive Source Volume Deletion)
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF
- **Description**: Create snapshots that survive deletion of the source volume
- **Features**:
  - Uses `zfs send | zfs receive` for full data copy
  - Stored as independent datasets in a dedicated folder
  - Source volume can be deleted without affecting the snapshot
  - Snapshots are stored under configurable parent dataset (default: `{pool}/csi-detached-snapshots`)
- **Parameters**:
  - `detachedSnapshots: "true"` in VolumeSnapshotClass
  - `detachedSnapshotsParentDataset` (optional) - where snapshots are stored
- **Use Cases**:
  - Backup/DR scenarios requiring snapshots that outlive source volumes
  - Data migration where source will be deleted
  - Long-term archival with independent snapshot lifecycle
  - Compliance requirements for independent backup copies

**Example VolumeSnapshotClass for Detached Snapshots:**
```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapshot-detached
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  detachedSnapshots: "true"
  detachedSnapshotsParentDataset: "tank/backups/csi-snapshots"  # optional
```

**Note:** Detached snapshots take longer to create than regular COW snapshots since they perform a full data copy via `zfs send/receive`. Use regular snapshots for fast point-in-time recovery, and detached snapshots when you need snapshots that survive source volume deletion.

**Example:**
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-pvc
spec:
  storageClassName: truenas-nfs
  dataSource:
    name: my-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
```

### Volume Health Monitoring
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF
- **Description**: Report volume health status to Kubernetes via CSI `ControllerGetVolume` capability
- **CSI Capability**: `GET_VOLUME` - enables Kubernetes to query volume health
- **Features**:
  - Reports `VolumeCondition` with `Abnormal` flag and descriptive `Message`
  - Health checks performed on-demand when Kubernetes queries volume status
  - Protocol-specific validation of underlying storage resources

**Health Checks Performed:**

| Protocol | Check | Abnormal If |
|----------|-------|-------------|
| NFS | Dataset exists | Dataset not found or inaccessible |
| NFS | NFS share enabled | Share disabled or missing |
| NVMe-oF | ZVOL exists | ZVOL not found |
| NVMe-oF | Subsystem exists | Subsystem missing |
| NVMe-oF | Namespace exists | Namespace not found in subsystem |

**Return Values:**
- `Abnormal: false` - Volume is healthy, all checks passed
- `Abnormal: true` - Volume has issues, `Message` contains details

**Use Cases:**
- Kubernetes can detect storage issues before pods fail
- Operators can monitor volume health via CSI events
- Automated alerting on storage problems

**Note:** This is a controller-side capability. Kubernetes periodically queries volume health for volumes with `GET_VOLUME` capability enabled.

## Infrastructure Features

### WebSocket API Client
- **Status**: ✅ Stable and functional
- **Description**: Resilient WebSocket client for TrueNAS API communication
- **Features**:
  - Automatic reconnection with exponential backoff
  - Ping/pong heartbeat (30-second intervals)
  - Read/write deadline management
  - Connection state tracking
  - Graceful error handling
- **Endpoints**: 
  - `wss://` for HTTPS (recommended)
  - `ws://` for HTTP (development only)

### Connection Resilience
- **Status**: ✅ Implemented and tested
- **Description**: Automatic recovery from network disruptions
- **Features**:
  - Exponential backoff for reconnections (1s → 2s → 4s → ... max 30s)
  - Operation retries during connectivity issues
  - State preservation across reconnections
  - Connection health monitoring
- **Testing**: Validated with manual connection disruption tests

### High Availability (Controller)
- **Status**: ✅ Supported
- **Description**: Multiple controller replicas for redundancy
- **Implementation**: Kubernetes leader election
- **Default**: Single controller (can be increased via Helm chart)

## Observability Features

### Metrics (Prometheus)
- **Status**: ✅ Fully implemented
- **Endpoint**: `/metrics` on port 8080 (configurable)
- **Available Metrics**:

#### CSI Operation Metrics
- `tns_csi_operations_total`: Counter of CSI operations by method and status
- `tns_csi_operations_duration_seconds`: Histogram of operation durations

#### Volume Operation Metrics
- `tns_volume_operations_total`: Counter by protocol, operation, and status
- `tns_volume_operations_duration_seconds`: Histogram of volume operation durations
- `tns_volume_capacity_bytes`: Gauge of provisioned volume sizes

#### WebSocket Metrics
- `tns_websocket_connected`: Connection status gauge (1=connected, 0=disconnected)
- `tns_websocket_reconnects_total`: Counter of reconnection attempts
- `tns_websocket_messages_total`: Counter by direction (sent/received)
- `tns_websocket_message_duration_seconds`: Histogram of API call durations
- `tns_websocket_connection_duration_seconds`: Current connection duration

### ServiceMonitor Support
- **Status**: ✅ Implemented
- **Description**: Automatic Prometheus Operator integration
- **Configuration**: Optional, enabled via Helm chart values

### Logging
- **Status**: ✅ Comprehensive logging
- **Levels**: Standard klog verbosity levels (--v=1 to --v=10)
- **Default**: v=2 (info level)
- **Components**:
  - Controller logs: Volume operations, API interactions
  - Node logs: Mount/unmount operations, device management
  - Structured logging with context

## Deployment Features

### Helm Chart
- **Status**: ✅ Production-ready chart
- **Registry**: 
  - Docker Hub (recommended): `oci://registry-1.docker.io/bfenski/tns-csi-driver`
  - GitHub Container Registry: `oci://ghcr.io/fenio/tns-csi-driver`
- **Features**:
  - Configurable resource limits
  - Multiple storage class support (NFS, NVMe-oF)
  - ServiceMonitor for Prometheus
  - RBAC configuration
  - Customizable mount options
  - Volume expansion enabled by default

### Storage Classes
- **Status**: ✅ Flexible configuration
- **Support**: Multiple storage classes per driver installation
- **Parameters**:
  - Common: `protocol`, `pool`, `server`, `deleteStrategy`
  - NFS-specific: `path`
  - NVMe-oF specific: `subsystemNQN`, `fsType`, `transport`, `port`
  - ZFS properties: See "Configurable ZFS Properties" section below
- **Mount Options**: Configurable via StorageClass `mountOptions` field (see "Configurable Mount Options" above)

### Configurable ZFS Properties
- **Status**: ✅ Implemented
- **Description**: Configure ZFS dataset/ZVOL properties via StorageClass parameters
- **Prefix**: All ZFS properties use the `zfs.` prefix in StorageClass parameters

#### NFS (Dataset) Properties
| Parameter | Description | Valid Values |
|-----------|-------------|--------------|
| `zfs.compression` | Compression algorithm | `off`, `lz4`, `gzip`, `gzip-1` to `gzip-9`, `zstd`, `zstd-1` to `zstd-19`, `lzjb`, `zle` |
| `zfs.dedup` | Deduplication | `off`, `on`, `verify`, `sha256`, `sha512` |
| `zfs.atime` | Access time updates | `on`, `off` |
| `zfs.sync` | Synchronous writes | `standard`, `always`, `disabled` |
| `zfs.recordsize` | Record size | `512`, `1K`, `2K`, `4K`, `8K`, `16K`, `32K`, `64K`, `128K`, `256K`, `512K`, `1M` |
| `zfs.copies` | Number of data copies | `1`, `2`, `3` |
| `zfs.snapdir` | Snapshot directory visibility | `hidden`, `visible` |
| `zfs.readonly` | Read-only mode | `on`, `off` |
| `zfs.exec` | Executable files | `on`, `off` |
| `zfs.aclmode` | ACL mode | `passthrough`, `restricted`, `discard`, `groupmask` |
| `zfs.acltype` | ACL type | `off`, `nfsv4`, `posix` |
| `zfs.casesensitivity` | Case sensitivity (creation only) | `sensitive`, `insensitive`, `mixed` |

#### NVMe-oF (ZVOL) Properties
| Parameter | Description | Valid Values |
|-----------|-------------|--------------|
| `zfs.compression` | Compression algorithm | `off`, `lz4`, `gzip`, `gzip-1` to `gzip-9`, `zstd`, `zstd-1` to `zstd-19`, `lzjb`, `zle` |
| `zfs.dedup` | Deduplication | `off`, `on`, `verify`, `sha256`, `sha512` |
| `zfs.sync` | Synchronous writes | `standard`, `always`, `disabled` |
| `zfs.copies` | Number of data copies | `1`, `2`, `3` |
| `zfs.readonly` | Read-only mode | `on`, `off` |
| `zfs.sparse` | Thin provisioning | `true`, `false` |
| `zfs.volblocksize` | Volume block size | `512`, `1K`, `2K`, `4K`, `8K`, `16K`, `32K`, `64K`, `128K` |

**Example StorageClass with ZFS Properties:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-compressed
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  # ZFS properties
  zfs.compression: "lz4"
  zfs.atime: "off"
  zfs.recordsize: "128K"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**Example NVMe-oF StorageClass with ZFS Properties:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nvmeof-compressed
provisioner: tns.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: truenas.local
  transport: tcp
  port: "4420"
  # ZFS properties
  zfs.compression: "lz4"
  zfs.sparse: "true"
  zfs.volblocksize: "16K"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### Volume Name Templating
- **Status**: ✅ Implemented
- **Description**: Customize volume/dataset names on TrueNAS using Go templates
- **Protocols**: NFS, NVMe-oF
- **Use Cases**:
  - Use meaningful names instead of auto-generated PV UUIDs
  - Include namespace/PVC name in dataset names for easier identification
  - Organize volumes with consistent naming patterns

#### Template Variables
| Variable | Description | Example Value |
|----------|-------------|---------------|
| `.PVCName` | PVC name | `postgres-data` |
| `.PVCNamespace` | PVC namespace | `production` |
| `.PVName` | PV name (CSI volume name) | `pvc-abc123-def456` |

#### StorageClass Parameters
| Parameter | Description | Example |
|-----------|-------------|---------|
| `nameTemplate` | Go template for full name | `{{ .PVCNamespace }}-{{ .PVCName }}` |
| `namePrefix` | Simple prefix | `prod-` |
| `nameSuffix` | Simple suffix | `-data` |

**Note**: `nameTemplate` takes precedence over `namePrefix`/`nameSuffix` if both are specified.

#### Name Sanitization
Volume names are automatically sanitized for ZFS compatibility:
- Invalid characters replaced with hyphens
- Leading/trailing hyphens removed
- Multiple consecutive hyphens collapsed
- Truncated to 63 characters (K8s label compatibility)

**Example StorageClass with Name Template:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-named
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  # Volume name templating
  nameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

With this StorageClass, a PVC named `postgres-data` in namespace `production` would create a dataset named `tank/production-postgres-data` instead of `tank/pvc-abc123-def456-789...`.

**Example with Simple Prefix/Suffix:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-prefixed
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  namePrefix: "k8s-"
  nameSuffix: "-vol"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### RBAC
- **Status**: ✅ Complete RBAC configuration
- **Components**:
  - ServiceAccounts for controller and node components
  - ClusterRoles with minimal required permissions
  - ClusterRoleBindings

## Testing Infrastructure

### CI/CD Pipeline
- **Status**: ✅ Fully automated
- **Platform**: GitHub Actions with self-hosted runner
- **Workflows**:
  - CI (lint, build, unit tests)
  - Integration tests (NFS and NVMe-oF)
  - Release automation
  - Dashboard generation

### Integration Tests
- **Status**: ✅ Comprehensive test suite
- **Infrastructure**: Self-hosted (k3s + real TrueNAS)
- **Test Scenarios**:
  - Basic volume provisioning and deletion (NFS, NVMe-oF)
  - Volume expansion (NFS, NVMe-oF)
  - Concurrent volume operations
  - StatefulSet workloads
  - Snapshot creation and restoration (NFS, NVMe-oF)
  - Connection resilience
  - Orphaned resource cleanup
  - Persistence testing
- **Execution**: Automatic on every push to main branch and pull requests

### Sanity Tests
- **Status**: ✅ CSI spec compliance testing
- **Framework**: csi-sanity test suite
- **Coverage**: Basic CSI operations validation

### Test Dashboard
- **Status**: ✅ Live dashboard
- **URL**: https://fenio.github.io/tns-csi/dashboard/
- **Features**: Test results history, trend analysis

## Security Features

### API Authentication
- **Status**: ✅ Secure API key authentication
- **Storage**: Kubernetes Secrets
- **Support**: TrueNAS API key authentication

### TLS Support
- **Status**: ✅ Supported
- **WebSocket**: WSS (WebSocket Secure) protocol
- **Recommended**: Always use `wss://` in production

### RBAC
- **Status**: ✅ Minimal privilege principle
- **Configuration**: Separate service accounts for controller and node components

## Kubernetes Feature Support

### Access Modes
- **NFS**:
  - ✅ ReadWriteMany (RWX) - Multiple pods on multiple nodes
  - ✅ ReadWriteOnce (RWO) - Single pod access
- **NVMe-oF**:
  - ✅ ReadWriteOnce (RWO) - Block storage limitation

### Volume Binding Modes
- ✅ Immediate - Volume provisioned immediately when PVC created
- ✅ WaitForFirstConsumer - Volume provisioned when pod scheduled

### Reclaim Policies
- ✅ Delete - Volume deleted when PVC removed (default)
- ✅ Retain - Volume kept on TrueNAS after PVC deletion

### Storage Classes
- ✅ Multiple storage classes per driver
- ✅ Default storage class support
- ✅ Custom parameters per class

## Platform Support

### Kubernetes Distributions
- ✅ **Tested**: k3s (self-hosted CI/CD)
- ✅ **Supported**: Standard Kubernetes 1.27+
- ⚠️ **Should Work**: 
  - kind (local development)
  - K0s, K3s, RKE2
  - Managed Kubernetes (EKS, GKE, AKS) - untested
- **Note**: Earlier Kubernetes versions (< 1.27) may work but are not tested

### Operating Systems
- ✅ **Linux**: Primary platform
  - Ubuntu 22.04+ (tested)
  - Debian-based distributions
  - RHEL/CentOS-based distributions
- ❌ **Windows**: Not supported (Linux-focused driver)
- ❌ **macOS**: Not supported as node OS (development on macOS works)

### Architectures
- ✅ **amd64** (x86_64): Fully supported
- ✅ **arm64**: Fully supported (tested on Apple Silicon via UTM)

### Container Runtimes
- ✅ containerd (primary)
- ✅ CRI-O
- ⚠️ Docker (should work, not extensively tested)

## TrueNAS Version Support

### Minimum Versions
- **NFS Support**: TrueNAS Scale 25.10+
- **NVMe-oF Support**: TrueNAS Scale 25.10+ (feature introduced in this version)

### API Compatibility
- **WebSocket API**: v2.0 (current endpoint: `/api/current`)
- **Authentication**: API key-based

### Required TrueNAS Configuration

#### For NFS
- NFS service enabled
- Network access from Kubernetes nodes
- ZFS pool with available space

#### For NVMe-oF
- **Static IP address** (DHCP not supported)
- **Pre-configured NVMe-oF subsystem** with:
  - At least one initial namespace (ZVOL)
  - TCP port configured (default: 4420)
  - Accessible from Kubernetes nodes
- NVMe-oF service enabled

## Known Limitations

### General
- **Production Readiness**: Early development, not production-ready
- **Testing Coverage**: Core features functional, extensive validation needed
- **Error Handling**: Improving, some edge cases may not be covered

### Protocol-Specific

#### NFS
- Network latency affects performance
- NFSv4.2 required (older versions not tested)
- Firewall rules must allow NFS ports

#### NVMe-oF
- Requires TrueNAS Scale 25.10+ (not available on TrueNAS CORE)
- Static IP mandatory (DHCP interfaces not shown in configuration)
- Subsystem must be pre-configured (driver doesn't create subsystems)
- Block storage only (ReadWriteOnce access mode)
- TCP transport only (RDMA not implemented)

### Snapshots
- Cross-protocol cloning not supported (NFS ↔ NVMe-oF)
- Cross-pool cloning not supported
- Restored volumes must be same size or larger

### Volume Expansion
- Shrinking not supported (ZFS limitation)
- Some operations may require volume to be unmounted

## Roadmap / Future Considerations

### Under Consideration (Not Committed)
- **Additional Protocols**: iSCSI or SMB support (low priority, based on community demand)
- **Multi-pool Support**: Advanced scheduling across multiple TrueNAS pools
- **Topology Awareness**: Multi-zone deployments
- **Volume Migration**: Move volumes between protocols/pools
- **Quota Management**: Advanced quota and reservation features

### Not Planned
- **iSCSI Protocol**: NVMe-oF is superior for block storage
- **Windows Support**: Linux-focused driver
- **Legacy Protocol Support**: Focus on modern protocols only

## Performance Characteristics

### NFS
- **Throughput**: Network-limited, typically 1-10 Gbps depending on network
- **Latency**: ~1-5ms additional latency vs local storage
- **IOPS**: Moderate (1000-10000 IOPS typical)
- **Best For**: Shared file storage, read-heavy workloads, multi-pod access

### NVMe-oF
- **Throughput**: Higher than NFS, can approach local NVMe speeds
- **Latency**: Lower than NFS (~100-500µs additional latency)
- **IOPS**: High (10000-100000+ IOPS depending on storage)
- **Best For**: Databases, high-performance applications, latency-sensitive workloads

### Snapshots
- **Creation Time**: Near-instant regardless of volume size
- **Space Overhead**: Minimal until data diverges
- **Restore Time**: Instant (clone operation)

## Documentation

### Available Documentation
- ✅ README.md - Project overview and quick start
- ✅ DEPLOYMENT.md - Detailed deployment guide
- ✅ QUICKSTART.md - NFS quick start guide
- ✅ QUICKSTART-NVMEOF.md - NVMe-oF setup guide
- ✅ SNAPSHOTS.md - Snapshot and cloning guide
- ✅ METRICS.md - Prometheus metrics documentation
- ✅ TESTING.md - Comprehensive testing guide and infrastructure details
- ✅ FEATURES.md - This document
- ✅ CONTRIBUTING.md - Contribution guidelines
- ✅ CONNECTION_RESILIENCE_TEST.md - Connection testing guide

### Helm Chart Documentation
- ✅ charts/tns-csi-driver/README.md - Complete Helm configuration reference
- ✅ charts/tns-csi-driver/values.yaml - Documented default values

## Testing Infrastructure

### Real Hardware, Real Tests

All features are tested on **real infrastructure** - not mocks or simulators:

**Test Environment:**
- ✅ Self-hosted GitHub Actions runner (dedicated Akamai/Linode infrastructure)
- ✅ Real Kubernetes clusters (k3s) provisioned for each test run
- ✅ Real TrueNAS Scale 25.10+ server with actual storage pools
- ✅ Real protocol operations (NFS mounts, NVMe-oF connections, actual I/O)

**CSI Specification Compliance:**
- ✅ Passes [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) v5.4.0 sanity tests
- ✅ Full CSI specification compliance verified

**Integration Test Coverage:**
- ✅ Basic volume operations (NFS & NVMe-oF)
- ✅ Volume expansion testing
- ✅ Snapshot creation and restoration
- ✅ StatefulSet volume management (3 replica testing)
- ✅ Data persistence across pod restarts
- ✅ Concurrent volume creation (5 simultaneous volumes)
- ✅ Connection resilience (WebSocket reconnection)
- ✅ Orphaned resource detection and cleanup

**Test Results:**
- View live dashboard: [Test Dashboard](https://fenio.github.io/tns-csi/dashboard/)
- CI status: [![Integration Tests](https://github.com/fenio/tns-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/integration.yml)

See [TESTING.md](TESTING.md) for comprehensive testing documentation.

## Getting Started

### Minimum Requirements
1. Kubernetes cluster 1.27+
2. TrueNAS Scale 25.10+
3. TrueNAS API key
4. Helm 3.0+
5. NFS client tools (NFS) or nvme-cli (NVMe-oF) on nodes

### Quick Install (NFS)
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

### Quick Install (NVMe-oF)
```bash
# Pre-requisite: Configure NVMe-oF subsystem in TrueNAS first!
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2025-01.com.truenas:csi"
```

## Support and Community

### Reporting Issues
- GitHub Issues: https://github.com/fenio/tns-csi/issues
- Include: Kubernetes version, TrueNAS version, logs, reproduction steps

### Contributing
- See CONTRIBUTING.md for guidelines
- Pull requests welcome
- Focus areas: Testing, documentation, bug fixes

### Status Updates
- Test Dashboard: https://fenio.github.io/tns-csi/dashboard/
- GitHub Actions: https://github.com/fenio/tns-csi/actions

---

**Last Updated**: 2025-12-17  
**Driver Version**: v0.0.x (early development)  
**Kubernetes Version Tested**: 1.27+  
**Go Version**: 1.25.5+
