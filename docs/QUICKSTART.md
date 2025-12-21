# TrueNAS CSI Driver - Quick Start Guide

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

## Prerequisites
- Kubernetes cluster (1.27+) (earlier versions may work but are not tested)
- Helm 3.0+
- **TrueNAS Scale 25.10 or later** (required for full feature support including NVMe-oF)
- TrueNAS API key (create in TrueNAS UI: Settings > API Keys)
- Storage pool available on TrueNAS
- For NFS: `nfs-common` package on all nodes (Debian/Ubuntu)

## Installation (Recommended: Helm)

### Quick Install with Helm

The fastest way to get started is using Helm from the OCI registry:

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

**Replace these values:**
- `YOUR-TRUENAS-IP` - Your TrueNAS server IP address
- `YOUR-API-KEY` - API key from TrueNAS (Settings > API Keys)
- `YOUR-POOL-NAME` - ZFS pool name (e.g., `tank`, `storage`)

That's it! The driver is now installed and ready to use.

### Verify Installation

```bash
# Check pods are running
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# Check storage class created
kubectl get storageclass truenas-nfs

# View controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver
```

### Alternative: Install from Local Chart

If you've cloned the repository:

```bash
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

For more Helm configuration options, see the [Helm Chart README](../charts/tns-csi-driver/README.md).

## Usage

### Creating a Persistent Volume Claim

Create a PVC (example: `my-pvc.yaml`):
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-app-data
  namespace: default
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
  storageClassName: tns-nfs
```

Apply:
```bash
kubectl apply -f my-pvc.yaml
```

Check status:
```bash
kubectl get pvc my-app-data
```

### Using PVC in a Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
spec:
  containers:
  - name: app
    image: nginx:latest
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-app-data
```

### Using PVC in a StatefulSet

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-statefulset
spec:
  serviceName: my-service
  replicas: 3
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
      - name: app
        image: nginx:latest
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ "ReadWriteOnce" ]
      storageClassName: tns-csi-nfs
      resources:
        requests:
          storage: 10Gi
```

## Verification Commands

### Check Driver Status
```bash
# Check controller pod
kubectl get pods -n kube-system | grep tns-csi-controller

# Check node pods
kubectl get pods -n kube-system | grep tns-csi-node

# View controller logs
kubectl logs -n kube-system tns-csi-controller-0 -c tns-csi-plugin

# View node logs
kubectl logs -n kube-system <node-pod-name> -c tns-csi-plugin
```

### Check Volumes
```bash
# List PVCs
kubectl get pvc -A

# List PVs
kubectl get pv

# Describe a specific PVC
kubectl describe pvc <pvc-name>

# Check volume mount in pod
kubectl exec <pod-name> -- df -h
```

### Test Data Persistence
```bash
# Write test data
kubectl exec <pod-name> -- sh -c "echo 'test data' > /data/test.txt"

# Read data
kubectl exec <pod-name> -- cat /data/test.txt

# Delete and recreate pod, verify data persists
kubectl delete pod <pod-name>
kubectl apply -f <pod-yaml>
kubectl exec <pod-name> -- cat /data/test.txt
```

## Troubleshooting

### Check Driver Status
```bash
# Check all pods are running
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# View controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver

# View node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver

# Check storage classes
kubectl get storageclass
```

### Common Issues

#### Connection Failed
- Verify TrueNAS URL format: `wss://YOUR-IP:443/api/current`
- Check API key has proper permissions (Settings > API Keys in TrueNAS UI)
- Verify network connectivity from cluster to TrueNAS
- Check TrueNAS API service is running
- For self-signed certificates, WebSocket URL must use `wss://` protocol

#### PVC Stuck in Pending
```bash
# Check storage class exists
kubectl get storageclass

# Check controller logs for errors
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver --tail=50

# Describe PVC for events
kubectl describe pvc <pvc-name>
```

#### Volume Mount Failed (NFS)
- Verify NFS service is enabled on TrueNAS
- Check firewall rules allow NFS traffic (ports 111, 2049)
- Verify `nfs-common` package is installed on nodes: `dpkg -l | grep nfs-common`
- Check node driver logs:
  ```bash
  kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver
  ```

#### "zpool (parentDataset) does not exist" Error
- If using `parentDataset` parameter, it must exist on TrueNAS
- Create it first: `zfs create tank/k8s-volumes` (via TrueNAS shell or API)
- Or omit `parentDataset` to create volumes directly in the pool

### Enable Debug Logging
```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --reuse-values \
  --set controller.extraArgs="{--v=5}" \
  --set node.extraArgs="{--v=5}"
```

## Storage Protocols

### NFS
- **Access Modes**: ReadWriteMany (RWX), ReadWriteOnce (RWO)
- **Mount Options**: NFSv4.2, nolock
- **Use Case**: Shared storage across multiple pods
- **Volume Expansion**: ✅ Implemented and functional
- **Status**: Functional, testing in progress

### NVMe-oF
- **Access Modes**: ReadWriteOnce (RWO)
- **Use Case**: High-performance block storage
- **Volume Expansion**: ✅ Implemented and functional
- **Status**: Functional, requires TrueNAS Scale 25.10+, testing in progress

## Advanced Configuration

### Using a Values File

For more complex configurations, create a `my-values.yaml` file:

```yaml
truenas:
  url: "wss://YOUR-TRUENAS-IP:443/api/current"
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
    isDefault: true  # Set as default storage class
    # Optional: Additional parameters
    parameters:
      # Keep volumes on TrueNAS when PVC is deleted (useful for data protection)
      # deleteStrategy: "retain"
      # ZFS properties
      # zfs.compression: "lz4"
      # zfs.recordsize: "128K"
```

Install with values file:
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --create-namespace \
  --values my-values.yaml
```

### Volume Retention (Delete Strategy)

To keep volumes on TrueNAS even when PVCs are deleted (useful for data protection):

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP" \
  --set "storageClasses.nfs.parameters.deleteStrategy=retain"
```

### Volume Expansion

To expand a volume (enabled by default with Helm):

```bash
# Patch the PVC to increase size
kubectl patch pvc my-pvc -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

The driver will automatically resize the dataset on TrueNAS.

### NVMe-oF Configuration

To use NVMe-oF instead of NFS:

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2025-01.com.truenas:csi"
```

**Requirements:**
- TrueNAS Scale 25.10+ (NVMe-oF feature introduced in 25.10)
- Pre-configured NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems)
  - The `subsystemNQN` parameter must match the subsystem you created
- Linux kernel with `nvme-tcp` module support
- Load module: `sudo modprobe nvme-tcp`
- TrueNAS NVMe-oF service configured and running

See [QUICKSTART-NVMEOF.md](QUICKSTART-NVMEOF.md) for detailed NVMe-oF setup instructions.

## Performance Considerations

### NFS Performance
- Uses NFSv4.2 for best performance and features
- `nolock` option reduces locking overhead
- Suitable for most workloads including databases (with proper configuration)

### Network
- Ensure low-latency network between Kubernetes nodes and TrueNAS
- Consider using dedicated storage network
- Monitor NFS mount statistics: `nfsstat -m`

## Security

### API Key Management
- Store API key in Kubernetes Secret
- Use RBAC to restrict secret access
- Rotate API keys regularly
- Use TrueNAS read-only API keys where possible (for future implementations)

### Network Security
- Use TLS for WebSocket connections (default: wss://)
- Implement network policies to restrict access
- Consider VPN or private network for storage traffic

## Monitoring

### Health Checks
```bash
# Check CSI driver health
kubectl get csidrivers

# Check pod health
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver
```

### Metrics
The driver exposes standard CSI metrics that can be scraped by Prometheus:
- Volume operations (create, delete, mount, unmount)
- Operation latencies
- Error rates

## Upgrading

To upgrade to a newer version:

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.5.0 \
  --namespace kube-system \
  --reuse-values
```

## Uninstalling

To remove the driver (this will NOT delete existing PVs):

```bash
# Delete PVCs first if you want to clean up volumes
kubectl delete pvc --all -A

# Uninstall the driver
helm uninstall tns-csi --namespace kube-system
```

## Snapshots and Cloning

The driver supports volume snapshots and cloning for both NFS and NVMe-oF protocols.

### Quick Snapshot Example

```bash
# Create a snapshot
kubectl apply -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-app-snapshot
spec:
  volumeSnapshotClassName: truenas-nfs-snapclass
  source:
    persistentVolumeClaimName: my-app-data
EOF

# Restore from snapshot
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-app-restored
spec:
  storageClassName: tns-nfs
  dataSource:
    name: my-app-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
EOF
```

For complete snapshot documentation including prerequisites, advanced features, and troubleshooting, see [SNAPSHOTS.md](SNAPSHOTS.md).

## Next Steps

- Review the [Helm Chart README](charts/tns-csi-driver/README.md) for detailed configuration options
- Check [SNAPSHOTS.md](SNAPSHOTS.md) for snapshot and cloning features
- Check [TESTING.md](TESTING.md) for comprehensive testing procedures
- See [DEPLOYMENT.md](DEPLOYMENT.md) for production deployment best practices

## Support

For issues or questions:
1. Check controller and node driver logs (see Troubleshooting section)
2. Review [TESTING.md](TESTING.md) for testing procedures and known working configurations
3. Create an issue with:
   - Kubernetes version
   - TrueNAS version
   - Driver logs
   - Steps to reproduce

## References

- [Helm Chart README](charts/tns-csi-driver/README.md) - Complete Helm configuration reference
- [CSI Specification](https://github.com/container-storage-interface/spec)
- [TrueNAS API Documentation](https://www.truenas.com/docs/api/)
- [Kubernetes Storage Documentation](https://kubernetes.io/docs/concepts/storage/)
