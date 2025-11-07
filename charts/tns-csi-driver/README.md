# TrueNAS CSI Driver Helm Chart

A Container Storage Interface (CSI) driver for TrueNAS that enables dynamic provisioning of storage volumes in Kubernetes clusters.

## Features

- **Dynamic Volume Provisioning**: Automatically create and delete storage volumes
- **Multiple Protocols**: Support for NFS and NVMe-oF
- **Volume Snapshots**: Create, delete, and restore from snapshots (both NFS and NVMe-oF)
- **Volume Cloning**: Create new volumes from existing snapshots
- **Volume Expansion**: Resize volumes without pod recreation (NFS)
- **WebSocket API**: Real-time communication with TrueNAS using WebSockets with automatic reconnection
- **Production Ready**: Connection resilience, proper cleanup, comprehensive error handling

## Prerequisites

- Kubernetes 1.20+
- Helm 3.0+
- TrueNAS SCALE 22.12+ or TrueNAS CORE 13.0+
- TrueNAS API key with appropriate permissions (create in TrueNAS UI: Settings > API Keys)
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu)
- For NVMe-oF: Linux kernel with nvme-tcp module support

## Installation

### Quick Start - NFS (Using OCI Registry)

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
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
```

Install with:
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
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

From OCI registry:
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

From local chart:
```bash
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --values charts/tns-csi-driver/values-nfs.yaml \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

#### NVMe-oF (Block storage, requires kernel modules)

From OCI registry:
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2025-01.com.truenas:csi"
```

**Important:** The `subsystemNQN` parameter is required and must match a pre-configured NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems).

From local chart:
```bash
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --values charts/tns-csi-driver/values-nvmeof.yaml \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="your-api-key" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2025-01.com.truenas:csi"
```

## Configuration

### TrueNAS Connection Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `truenas.url` | WebSocket URL (wss://host:port/api/current) | `""` (required) |
| `truenas.apiKey` | TrueNAS API key | `""` (required) |

### Storage Class Configuration - NFS

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses.nfs.enabled` | Enable NFS storage class | `false` |
| `storageClasses.nfs.name` | Storage class name | `truenas-nfs` |
| `storageClasses.nfs.pool` | ZFS pool name on TrueNAS | `""` (required) |
| `storageClasses.nfs.server` | TrueNAS server IP for NFS mounts | `""` (required) |
| `storageClasses.nfs.parentDataset` | Parent dataset (optional, must exist) | `""` |
| `storageClasses.nfs.reclaimPolicy` | Reclaim policy (Delete/Retain) | `Delete` |
| `storageClasses.nfs.allowVolumeExpansion` | Enable volume expansion | `true` |
| `storageClasses.nfs.volumeBindingMode` | Binding mode | `Immediate` |
| `storageClasses.nfs.isDefault` | Set as default storage class | `false` |
| `storageClasses.nfs.mountOptions` | NFS mount options | `[hard, nfsvers=4.1]` |

**Important Note on `parentDataset`:**
- If `parentDataset` is specified, it must already exist on TrueNAS
- The full path would be `pool/parentDataset` (e.g., `tank/k8s-volumes`)
- If empty or omitted, volumes will be created directly in the pool
- To create volumes in a subdirectory, create the dataset on TrueNAS first:
  ```bash
  # On TrueNAS via API or UI
  zfs create tank/k8s-volumes
  ```

### Storage Class Configuration - NVMe-oF

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses.nvmeof.enabled` | Enable NVMe-oF storage class | `false` |
| `storageClasses.nvmeof.name` | Storage class name | `truenas-nvmeof` |
| `storageClasses.nvmeof.pool` | ZFS pool name on TrueNAS | `""` (required) |
| `storageClasses.nvmeof.server` | TrueNAS server IP | `""` (required) |
| `storageClasses.nvmeof.subsystemNQN` | Pre-configured NVMe-oF subsystem NQN | `""` (required) |
| `storageClasses.nvmeof.parentDataset` | Parent dataset (optional) | `""` |
| `storageClasses.nvmeof.transport` | Transport protocol (tcp/rdma) | `tcp` |
| `storageClasses.nvmeof.port` | NVMe-oF port | `4420` |
| `storageClasses.nvmeof.reclaimPolicy` | Reclaim policy | `Delete` |
| `storageClasses.nvmeof.allowVolumeExpansion` | Enable volume expansion | `true` |
| `storageClasses.nvmeof.volumeBindingMode` | Binding mode | `Immediate` |

**Important:** The `subsystemNQN` parameter is required for NVMe-oF volumes. You must pre-configure an NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems) before provisioning volumes. The CSI driver creates namespaces within this shared subsystem.

### Controller Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.replicaCount` | Number of controller replicas | `1` |
| `controller.resources.limits.cpu` | CPU limit | `200m` |
| `controller.resources.limits.memory` | Memory limit | `256Mi` |
| `controller.resources.requests.cpu` | CPU request | `100m` |
| `controller.resources.requests.memory` | Memory request | `128Mi` |

### Node Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `node.resources.limits.cpu` | CPU limit | `200m` |
| `node.resources.limits.memory` | Memory limit | `256Mi` |
| `node.resources.requests.cpu` | CPU request | `100m` |
| `node.resources.requests.memory` | Memory request | `128Mi` |

### Image Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | CSI driver image repository | `bfenski/tns-csi` |
| `image.tag` | Image tag | `v0.0.1` |
| `image.pullPolicy` | Image pull policy | `Always` |

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
  storageClassName: truenas-nfs
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

## Upgrading

To upgrade the chart from OCI registry:

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --reuse-values
```

Or with new values:

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --values my-values.yaml
```

From local chart:

```bash
helm upgrade tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --reuse-values
```

## Uninstalling

To uninstall/delete the `tns-csi` deployment:

```bash
helm uninstall tns-csi --namespace kube-system
```

**Note**: This will not delete existing PersistentVolumes. Delete PVCs first if you want to clean up volumes:

```bash
# Delete all PVCs using tns-csi storage classes
kubectl delete pvc -l storageclass=truenas-nfs

# Then uninstall
helm uninstall tns-csi --namespace kube-system
```

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
- For self-signed certificates, WebSocket URL must use `wss://` protocol

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
- Check TrueNAS NVMe-oF service is configured and running
- Verify firewall allows port 4420

### Enable Debug Logging

Add verbose logging to troubleshoot issues:

```bash
helm upgrade tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --reuse-values \
  --set controller.extraArgs="{--v=5}" \
  --set node.extraArgs="{--v=5}"
```

## Development

### Testing Changes

```bash
# Lint the chart
helm lint charts/tns-csi-driver

# Render templates locally
helm template tns-csi charts/tns-csi-driver \
  --namespace kube-system \
  --values my-values.yaml

# Dry-run installation
helm install tns-csi charts/tns-csi-driver \
  --namespace kube-system \
  --values my-values.yaml \
  --dry-run --debug
```

### Package and Publish Chart

Package the chart locally:
```bash
helm package charts/tns-csi-driver
```

Push to OCI registry (Docker Hub):
```bash
helm push tns-csi-driver-0.0.1.tgz oci://registry-1.docker.io/bfenski
```

Pull chart from OCI registry:
```bash
helm pull oci://registry-1.docker.io/bfenski/tns-csi-driver --version 0.0.1
```

## Support

- **Issues**: https://github.com/fenio/tns-csi/issues
- **Discussions**: https://github.com/fenio/tns-csi/discussions
- **Documentation**: https://github.com/fenio/tns-csi

## License

GPL-3.0 - See [LICENSE](../../LICENSE) for details
