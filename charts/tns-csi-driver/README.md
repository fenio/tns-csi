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
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
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
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
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
  nfs:
    enabled: true
    name: truenas-nfs
    pool: "tank"
    server: "YOUR-TRUENAS-IP"
    # Optional: specify parent dataset (must exist on TrueNAS)
    # parentDataset: "k8s-volumes"
    mountOptions:
      - hard
      - nfsvers=4.1
      - noatime

  nvmeof:
    enabled: false

  iscsi:
    enabled: false
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
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

#### NVMe-oF (Block storage, requires kernel modules)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP"
```

The driver automatically creates a dedicated NVMe-oF subsystem for each volume. No pre-configured subsystem is needed — only a port must be configured in TrueNAS (Shares > NVMe-oF Targets > Ports).

#### iSCSI (Block storage, broad compatibility)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.iscsi.enabled=true \
  --set storageClasses.iscsi.pool="YOUR-POOL-NAME" \
  --set storageClasses.iscsi.server="YOUR-TRUENAS-IP"
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

### Storage Class Configuration - NFS

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses.nfs.enabled` | Enable NFS storage class | `true` |
| `storageClasses.nfs.name` | Storage class name | `tns-csi-nfs` |
| `storageClasses.nfs.pool` | ZFS pool name on TrueNAS | `"storage"` |
| `storageClasses.nfs.server` | TrueNAS server IP for NFS mounts | `""` (required) |
| `storageClasses.nfs.parentDataset` | Parent dataset (optional, must exist) | `""` |
| `storageClasses.nfs.isDefault` | Set as default storage class | `false` |
| `storageClasses.nfs.reclaimPolicy` | Reclaim policy (Delete/Retain) | `Delete` |
| `storageClasses.nfs.volumeBindingMode` | Binding mode | `Immediate` |
| `storageClasses.nfs.allowVolumeExpansion` | Enable volume expansion | `true` |
| `storageClasses.nfs.mountOptions` | NFS mount options (merged with defaults) | `[]` |
| `storageClasses.nfs.parameters` | Additional StorageClass parameters | `{}` |

**Additional NFS Parameters (via `parameters` map):**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `deleteStrategy` | Volume deletion behavior: `delete` or `retain` | `delete` |
| `nameTemplate` | Go template for volume names (e.g., `{{ .PVCNamespace }}-{{ .PVCName }}`) | (auto) |
| `namePrefix` | Prefix to prepend to volume name | `""` |
| `nameSuffix` | Suffix to append to volume name | `""` |
| `markAdoptable` | Mark new volumes as adoptable for cluster migration | `"false"` |
| `adoptExisting` | Adopt existing TrueNAS volumes matching PVC name | `"false"` |
| `encryption` | Enable ZFS native encryption | `"false"` |
| `encryptionAlgorithm` | Encryption algorithm | `"AES-256-GCM"` |
| `encryptionGenerateKey` | Auto-generate encryption key | `"false"` |
| `zfs.compression` | ZFS compression algorithm (e.g., `lz4`, `zstd`, `off`) | (inherited) |
| `zfs.dedup` | ZFS deduplication | (inherited) |
| `zfs.atime` | Access time updates | (inherited) |
| `zfs.sync` | Sync writes | (inherited) |
| `zfs.recordsize` | ZFS record size | (inherited) |

See [FEATURES.md](../../docs/FEATURES.md) for complete ZFS property documentation.

**Important Note on `parentDataset`:**
- If `parentDataset` is specified, it must already exist on TrueNAS
- The full path would be `pool/parentDataset` (e.g., `tank/k8s-volumes`)
- If empty or omitted, volumes will be created directly in the pool

### Storage Class Configuration - NVMe-oF

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses.nvmeof.enabled` | Enable NVMe-oF storage class | `false` |
| `storageClasses.nvmeof.name` | Storage class name | `tns-csi-nvmeof` |
| `storageClasses.nvmeof.pool` | ZFS pool name on TrueNAS | `"storage"` |
| `storageClasses.nvmeof.server` | TrueNAS server IP | `""` (required) |
| `storageClasses.nvmeof.parentDataset` | Parent dataset (optional) | `""` |
| `storageClasses.nvmeof.transport` | Transport protocol (tcp/rdma) | `tcp` |
| `storageClasses.nvmeof.port` | NVMe-oF port | `4420` |
| `storageClasses.nvmeof.fsType` | Filesystem type (ext4/xfs) | `ext4` |
| `storageClasses.nvmeof.isDefault` | Set as default storage class | `false` |
| `storageClasses.nvmeof.reclaimPolicy` | Reclaim policy | `Delete` |
| `storageClasses.nvmeof.volumeBindingMode` | Binding mode | `Immediate` |
| `storageClasses.nvmeof.allowVolumeExpansion` | Enable volume expansion | `true` |
| `storageClasses.nvmeof.mountOptions` | Filesystem mount options (merged with defaults) | `[]` |
| `storageClasses.nvmeof.parameters` | Additional StorageClass parameters | `{}` |

**Additional NVMe-oF Parameters (via `parameters` map):**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `deleteStrategy` | Volume deletion behavior: `delete` or `retain` | `delete` |
| `portID` | TrueNAS NVMe-oF port ID (auto-detected if not set) | (auto) |
| `nameTemplate` | Go template for volume names | (auto) |
| `namePrefix` | Prefix to prepend to volume name | `""` |
| `nameSuffix` | Suffix to append to volume name | `""` |
| `markAdoptable` | Mark new volumes as adoptable for cluster migration | `"false"` |
| `adoptExisting` | Adopt existing TrueNAS volumes matching PVC name | `"false"` |
| `encryption` | Enable ZFS native encryption | `"false"` |
| `encryptionAlgorithm` | Encryption algorithm | `"AES-256-GCM"` |
| `encryptionGenerateKey` | Auto-generate encryption key | `"false"` |
| `zfs.compression` | ZFS compression algorithm | (inherited) |
| `zfs.dedup` | ZFS deduplication | (inherited) |
| `zfs.sync` | Sync writes | (inherited) |
| `zfs.volblocksize` | ZVOL block size | (inherited) |

The driver automatically creates a dedicated NVMe-oF subsystem per volume. No shared subsystem configuration is needed.

### Storage Class Configuration - iSCSI

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses.iscsi.enabled` | Enable iSCSI storage class | `false` |
| `storageClasses.iscsi.name` | Storage class name | `tns-csi-iscsi` |
| `storageClasses.iscsi.pool` | ZFS pool name on TrueNAS | `"storage"` |
| `storageClasses.iscsi.server` | TrueNAS server IP | `""` (required) |
| `storageClasses.iscsi.parentDataset` | Parent dataset (optional) | `""` |
| `storageClasses.iscsi.port` | iSCSI port | `3260` |
| `storageClasses.iscsi.fsType` | Filesystem type (ext4/xfs) | `ext4` |
| `storageClasses.iscsi.isDefault` | Set as default storage class | `false` |
| `storageClasses.iscsi.reclaimPolicy` | Reclaim policy | `Delete` |
| `storageClasses.iscsi.volumeBindingMode` | Binding mode | `Immediate` |
| `storageClasses.iscsi.allowVolumeExpansion` | Enable volume expansion | `true` |
| `storageClasses.iscsi.mountOptions` | Filesystem mount options (merged with defaults) | `[]` |
| `storageClasses.iscsi.parameters` | Additional StorageClass parameters | `{}` |

**Additional iSCSI Parameters (via `parameters` map):**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `deleteStrategy` | Volume deletion behavior: `delete` or `retain` | `delete` |
| `nameTemplate` | Go template for volume names | (auto) |
| `namePrefix` | Prefix to prepend to volume name | `""` |
| `nameSuffix` | Suffix to append to volume name | `""` |
| `markAdoptable` | Mark new volumes as adoptable for cluster migration | `"false"` |
| `adoptExisting` | Adopt existing TrueNAS volumes matching PVC name | `"false"` |
| `encryption` | Enable ZFS native encryption | `"false"` |
| `encryptionAlgorithm` | Encryption algorithm | `"AES-256-GCM"` |
| `encryptionGenerateKey` | Auto-generate encryption key | `"false"` |
| `zfs.compression` | ZFS compression algorithm | (inherited) |
| `zfs.dedup` | ZFS deduplication | (inherited) |
| `zfs.sync` | Sync writes | (inherited) |
| `zfs.volblocksize` | ZVOL block size | (inherited) |

The driver automatically creates a dedicated iSCSI target per volume. Only an iSCSI portal must be configured in TrueNAS.

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
  nfs:
    parameters:
      encryption: "true"
      encryptionGenerateKey: "true"  # TrueNAS manages the key
```

For passphrase-based encryption, create a Secret and reference it:

```yaml
storageClasses:
  nfs:
    parameters:
      encryption: "true"
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
