# TrueNAS Scale CSI Driver Helm Chart

A Container Storage Interface (CSI) driver for TrueNAS Scale 25.10+ that enables dynamic provisioning of storage volumes in Kubernetes clusters.

## Features

- **Dynamic Volume Provisioning**: Automatically create and delete storage volumes
- **Multiple Protocols**: Support for NFS, NVMe-oF, and iSCSI
- **Volume Snapshots**: Create, delete, and restore from snapshots (all protocols)
- **Detached Snapshots**: Independent snapshot copies that survive source volume deletion
- **Volume Cloning**: Create new volumes from existing snapshots
- **Volume Expansion**: Resize volumes without pod recreation
- **Volume Retention**: Optional `deleteStrategy: retain` to keep volumes on PVC deletion
- **Volume Adoption**: Migrate volumes between clusters with `markAdoptable` / `adoptExisting`
- **Volume Name Templating**: Customize volume names with Go templates or prefix/suffix
- **ZFS Native Encryption**: Per-volume encryption with passphrase, hex key, or auto-generated keys
- **Configurable Mount Options**: Customize mount options via StorageClass `mountOptions`
- **Configurable ZFS Properties**: Set compression, dedup, recordsize, etc. via StorageClass parameters
- **WebSocket API**: Real-time communication with TrueNAS using WebSockets with automatic reconnection
- **Production Ready**: Connection resilience, proper cleanup, comprehensive error handling

## Prerequisites

- Kubernetes 1.20+
- Helm 3.0+
- **TrueNAS Scale 25.10 or later** (required for full feature support including NVMe-oF)
- TrueNAS API key with appropriate permissions (create in TrueNAS UI: Settings > API Keys)
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu)
- For NVMe-oF: Linux kernel with nvme-tcp module support, NVMe-oF port configured in TrueNAS (Shares > NVMe-oF Targets > Ports)
- For iSCSI: `open-iscsi` on all nodes, iSCSI portal configured in TrueNAS (Shares > iSCSI)
- For Snapshots: VolumeSnapshot CRDs installed in the cluster (see [Snapshot Configuration](#snapshot-configuration))

## Installation

### Quick Start - NFS (Using OCI Registry)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP"
```

Replace:
- `YOUR-TRUENAS-IP` - TrueNAS server IP address
- `YOUR-API-KEY` - API key from TrueNAS (Settings > API Keys)
- `YOUR-POOL-NAME` - ZFS pool name (e.g., `tank`, `storage`)

### Installation from Local Chart

If you've cloned the repository, you can install from the local chart:

```bash
helm install tns-csi ./charts/tns-csi-driver -n kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP"
```

### Installation with Values File

Create a `my-values.yaml` file:

```yaml
truenas:
  # WebSocket URL format: wss://<host>:<port>/api/current
  url: "wss://YOUR-TRUENAS-IP:443/api/current"
  # API key from TrueNAS UI
  apiKey: "1-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

storageClasses:
  - name: truenas-nfs
    enabled: true
    protocol: nfs
    pool: "tank"
    server: "YOUR-TRUENAS-IP"
    # Optional: specify parent dataset (must exist on TrueNAS)
    # parentDataset: "k8s-volumes"
    mountOptions:
      - hard
      - nfsvers=4.1
      - noatime
```

Install with:
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --values my-values.yaml
```

Or from local chart:
```bash
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --values my-values.yaml
```

### Example Configurations

#### NFS Only (Recommended for most use cases)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP"
```

#### NVMe-oF (Block storage, requires kernel modules)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses[1].enabled=true \
  --set storageClasses[1].pool="YOUR-POOL-NAME" \
  --set storageClasses[1].server="YOUR-TRUENAS-IP"
```

The driver automatically creates a dedicated NVMe-oF subsystem for each volume. No pre-configured subsystem is needed — only a port must be configured in TrueNAS (Shares > NVMe-oF Targets > Ports).

#### iSCSI (Block storage, broad compatibility)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses[2].enabled=true \
  --set storageClasses[2].pool="YOUR-POOL-NAME" \
  --set storageClasses[2].server="YOUR-TRUENAS-IP"
```

The driver automatically creates a dedicated iSCSI target for each volume. Only an iSCSI portal must be configured in TrueNAS (Shares > iSCSI).

### Using Kustomize

Each GitHub release includes a pre-rendered manifest (`tns-csi-driver-<version>.yaml`) with all protocols enabled and placeholder values. Download it and use Kustomize patches to replace `TRUENAS_IP` and `REPLACE_WITH_API_KEY`, and remove storage classes you don't need.

## Configuration

### TrueNAS Connection Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `truenas.url` | WebSocket URL (wss://host:port/api/current) | `""` (required) |
| `truenas.apiKey` | TrueNAS API key | `""` (required) |
| `truenas.existingSecret` | Name of existing Secret with `url` and `api-key` keys | `""` |
| `truenas.skipTLSVerify` | Skip TLS certificate verification | `false` |

### Storage Class Configuration

`storageClasses` is a list. Each entry creates a Kubernetes StorageClass. You can have multiple entries with the same protocol (e.g., two NFS classes with different reclaim policies). The default values file includes three entries (NFS enabled, NVMe-oF and iSCSI disabled). Add more entries to the list as needed.

**Common Fields (all protocols):**

| Field | Description | Default |
|-------|-------------|---------|
| `name` | StorageClass name (required) | — |
| `protocol` | Protocol: `nfs`, `nvmeof`, or `iscsi` (required) | — |
| `enabled` | Create this StorageClass | `true` |
| `pool` | ZFS pool name on TrueNAS (required) | `"storage"` |
| `server` | TrueNAS server IP/hostname (required) | `""` |
| `parentDataset` | Parent dataset (optional, must exist) | `""` |
| `isDefault` | Set as default storage class | `false` |
| `reclaimPolicy` | Reclaim policy (Delete/Retain) | `Delete` |
| `volumeBindingMode` | Binding mode | `Immediate` |
| `allowVolumeExpansion` | Enable volume expansion | `true` |
| `mountOptions` | Mount options (merged with driver defaults) | `[]` |
| `deleteStrategy` | Volume deletion behavior: `delete` or `retain` | `""` |
| `nameTemplate` | Go template for volume names | `""` |
| `namePrefix` | Prefix to prepend to volume name | `""` |
| `nameSuffix` | Suffix to append to volume name | `""` |
| `commentTemplate` | Go template for dataset comment visible in TrueNAS UI | `""` |
| `markAdoptable` | Mark new volumes as adoptable for cluster migration | `""` |
| `adoptExisting` | Adopt existing TrueNAS volumes matching PVC name | `""` |
| `encryption` | Enable ZFS native encryption | `""` |
| `encryptionAlgorithm` | Encryption algorithm | `""` |
| `encryptionGenerateKey` | Auto-generate encryption key | `""` |
| `parameters` | Additional StorageClass parameters (ZFS properties, etc.) | `{}` |

**NVMe-oF-specific Fields:**

| Field | Description | Default |
|-------|-------------|---------|
| `transport` | Transport protocol (tcp/rdma) | `tcp` |
| `port` | NVMe-oF port | `4420` |
| `fsType` | Filesystem type (ext4/xfs) | `ext4` |

**iSCSI-specific Fields:**

| Field | Description | Default |
|-------|-------------|---------|
| `port` | iSCSI port | `3260` |
| `fsType` | Filesystem type (ext4/xfs) | `ext4` |

**Additional Parameters (via `parameters` map):**

| Parameter | Description | Protocols |
|-----------|-------------|-----------|
| `zfs.compression` | ZFS compression algorithm (e.g., `lz4`, `zstd`, `off`) | all |
| `zfs.dedup` | ZFS deduplication | all |
| `zfs.atime` | Access time updates | nfs |
| `zfs.sync` | Sync writes | all |
| `zfs.recordsize` | ZFS record size | nfs |
| `zfs.volblocksize` | ZVOL block size (e.g., `16K`, `64K`) | nvmeof, iscsi |
| `portID` | TrueNAS NVMe-oF port ID (auto-detected if not set) | nvmeof |

See [FEATURES.md](../../docs/FEATURES.md) for complete ZFS property documentation.

**Important Note on `parentDataset`:**
- If `parentDataset` is specified, it must already exist on TrueNAS
- The full path would be `pool/parentDataset` (e.g., `tank/k8s-volumes`)
- If empty or omitted, volumes will be created directly in the pool

#### Multiple Storage Classes per Protocol

To create multiple classes of the same protocol (e.g., Delete + Retain NFS classes), add more entries to the list:

```yaml
storageClasses:
  - name: tns-csi-nfs
    protocol: nfs
    pool: "tank"
    server: "10.0.0.1"
    reclaimPolicy: Delete

  - name: tns-csi-nfs-retain
    protocol: nfs
    pool: "tank"
    server: "10.0.0.1"
    reclaimPolicy: Retain
    deleteStrategy: retain
```

### Snapshot Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `snapshots.enabled` | Enable snapshot support (adds csi-snapshotter sidecar) | `false` |
| `snapshots.volumeSnapshotClass.create` | Create VolumeSnapshotClass resources | `true` |
| `snapshots.volumeSnapshotClass.deletionPolicy` | Deletion policy (Delete/Retain) | `Delete` |
| `snapshots.detached.enabled` | Enable detached snapshot classes | `false` |
| `snapshots.detached.parentDataset` | Parent dataset for detached snapshots | `{pool}/csi-detached-snapshots` |
| `snapshots.detached.deletionPolicy` | Deletion policy for detached snapshots | `Delete` |

**Prerequisites:** VolumeSnapshot CRDs must be installed before enabling snapshots:
```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
```

Detached snapshots use `zfs send/receive` to create independent dataset copies that survive deletion of the source volume, useful for backup and disaster recovery.

### Controller Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.replicas` | Number of controller replicas | `1` |
| `controller.logLevel` | Log verbosity (0-5) | `2` |
| `controller.debug` | Enable debug mode | `false` |
| `controller.metrics.enabled` | Enable Prometheus metrics | `true` |
| `controller.metrics.port` | Metrics port | `8080` |
| `controller.resources.limits.cpu` | CPU limit | `200m` |
| `controller.resources.limits.memory` | Memory limit | `200Mi` |
| `controller.resources.requests.cpu` | CPU request | `10m` |
| `controller.resources.requests.memory` | Memory request | `20Mi` |

### Node Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `node.kubeletPath` | Kubelet data directory | `/var/lib/kubelet` |
| `node.logLevel` | Log verbosity (0-5) | `2` |
| `node.debug` | Enable debug mode | `false` |
| `node.resources.limits.cpu` | CPU limit | `200m` |
| `node.resources.limits.memory` | Memory limit | `200Mi` |
| `node.resources.requests.cpu` | CPU request | `10m` |
| `node.resources.requests.memory` | Memory request | `20Mi` |

### Image Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | CSI driver image repository | `bfenski/tns-csi` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |

## Usage

### Creating a PersistentVolumeClaim

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
spec:
  accessModes:
    - ReadWriteMany  # NFS supports RWX
  resources:
    requests:
      storage: 10Gi
  storageClassName: tns-csi-nfs
```

### Using in a Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
spec:
  containers:
  - name: app
    image: nginx
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-pvc
```

### Volume Expansion

To resize a volume, edit the PVC:

```bash
kubectl patch pvc my-pvc -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

The volume will be automatically resized on TrueNAS (if `allowVolumeExpansion: true`).

### Encryption

To use ZFS native encryption, set the encryption parameters in your StorageClass:

```yaml
storageClasses:
  - name: tns-csi-nfs
    protocol: nfs
    pool: "tank"
    server: "10.0.0.1"
    encryption: "true"
    encryptionGenerateKey: "true"  # TrueNAS manages the key
```

For passphrase-based encryption, create a Secret and reference it:

```yaml
storageClasses:
  - name: tns-csi-nfs
    protocol: nfs
    pool: "tank"
    server: "10.0.0.1"
    encryption: "true"
    parameters:
      csi.storage.k8s.io/provisioner-secret-name: my-encryption-secret
      csi.storage.k8s.io/provisioner-secret-namespace: kube-system
```

The Secret should contain either `encryptionPassphrase` (min 8 chars) or `encryptionKey` (64-char hex for 256-bit).

## Upgrading

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --reuse-values
```

Or with new values:

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --values my-values.yaml
```

### Migrating to v0.10.0 (from v0.9.x)

v0.10.0 changes `storageClasses` from a **map** (keyed by protocol) to a **list**. This enables multiple StorageClasses per protocol but requires updating your values file before upgrading.

**This change does not affect existing volumes, snapshots, or data.** Only the Helm values format changed — the CSI driver code is unchanged. As long as your StorageClass names stay the same, all existing PVCs and PVs continue working.

#### Step 1: Update your values file

Convert each map entry to a list item by adding `- ` prefix and an explicit `protocol` field:

**Before (v0.9.x):**

```yaml
storageClasses:
  nfs:
    enabled: true
    name: tns-csi-nfs
    pool: "tank"
    server: "10.0.0.1"
    reclaimPolicy: Delete
  nvmeof:
    enabled: false
  iscsi:
    enabled: false
```

**After (v0.10.0):**

```yaml
storageClasses:
  - name: tns-csi-nfs
    enabled: true
    protocol: nfs
    pool: "tank"
    server: "10.0.0.1"
    reclaimPolicy: Delete
```

Key differences:
- Each entry is a list item (starts with `- `)
- `protocol` is now an explicit field (was the map key before)
- Disabled protocols can simply be omitted instead of set to `enabled: false`
- All other fields (`pool`, `server`, `reclaimPolicy`, `mountOptions`, `parameters`, etc.) stay exactly the same

#### Step 2: Update `--set` flags (if used instead of a values file)

If you use `--set` flags instead of a values file, update them to use array indexing:

| Before | After |
|--------|-------|
| `--set storageClasses.nfs.enabled=true` | `--set storageClasses[0].enabled=true` |
| `--set storageClasses.nfs.pool=tank` | `--set storageClasses[0].pool=tank` |
| `--set storageClasses.nfs.server=10.0.0.1` | `--set storageClasses[0].server=10.0.0.1` |
| | `--set storageClasses[0].protocol=nfs` (new, required) |
| | `--set storageClasses[0].name=tns-csi-nfs` (new, required) |

#### Step 3: Upgrade

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --values my-values.yaml
```

**Important:** Do not use `--reuse-values` for this upgrade — the old map-format values are incompatible with the new chart and will cause template errors. Pass your updated values file explicitly.

## Uninstalling

To uninstall/delete the `tns-csi` deployment:

```bash
helm uninstall tns-csi --namespace kube-system
```

**Note**: This will not delete existing PersistentVolumes. Delete PVCs first if you want to clean up volumes.

## Troubleshooting

### Check Driver Status

```bash
# Check controller pod
kubectl get pods -n kube-system -l app.kubernetes.io/component=controller

# Check node pods
kubectl get pods -n kube-system -l app.kubernetes.io/component=node

# Verify CSI driver registration
kubectl get csidrivers

# Check storage classes
kubectl get storageclass
```

### View Logs

```bash
# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver

# CSI provisioner logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c csi-provisioner
```

### Common Issues

#### Connection Failed
- Verify TrueNAS host and port are correct in `truenas.url`
- Check API key has proper permissions (create in TrueNAS UI: Settings > API Keys)
- Verify network connectivity from cluster to TrueNAS
- Check TrueNAS API service is running
- For self-signed certificates, set `truenas.skipTLSVerify: true`

#### Volume Creation Failed: "zpool (parentDataset) does not exist"
- The `parentDataset` value must point to an existing dataset on TrueNAS
- Either create the dataset on TrueNAS first, or remove the `parentDataset` parameter
- Example: If using `parentDataset: kubevols` and `pool: tank`, create `tank/kubevols` first

#### Volume Mount Failed (NFS)
- Verify NFS service is enabled on TrueNAS
- Check firewall rules allow NFS traffic (ports 111, 2049)
- Verify nfs-common package is installed on nodes: `dpkg -l | grep nfs-common`
- Check mount options are compatible with your NFS version

#### NVMe-oF Connection Failed
- Verify nvme-tcp kernel module is loaded: `lsmod | grep nvme_tcp`
- Load module if needed: `sudo modprobe nvme-tcp`
- Check that an NVMe-oF port is configured in TrueNAS (Shares > NVMe-oF Targets > Ports)
- Verify firewall allows port 4420

#### iSCSI Connection Failed
- Verify open-iscsi is installed on nodes: `dpkg -l | grep open-iscsi`
- Check that an iSCSI portal is configured in TrueNAS (Shares > iSCSI)
- Verify firewall allows port 3260

### Enable Debug Logging

The CSI driver uses log levels to control verbosity:

| Level | Description |
|-------|-------------|
| 0 | Errors only |
| 2 | Normal operation (default) - volume created/deleted messages |
| 4 | Detailed operations - API calls, staging details |
| 5 | Debug - request/response bodies, context dumps |

```bash
helm upgrade tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --reuse-values \
  --set controller.debug=true \
  --set node.debug=true
```

## Support

- **Issues**: https://github.com/fenio/tns-csi/issues
- **Discussions**: https://github.com/fenio/tns-csi/discussions
- **Documentation**: https://github.com/fenio/tns-csi

## License

GPL-3.0 - See [LICENSE](../../LICENSE) for details
