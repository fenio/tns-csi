# Quick Start: iSCSI Block Storage

**WARNING: EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

This guide explains how to set up and use iSCSI block storage with the TrueNAS CSI driver.

## Overview

iSCSI provides traditional block storage over TCP/IP networks. It's a mature protocol with broad compatibility, making it a good choice when:

- NVMe-oF is not available or supported
- You need block storage with wide ecosystem compatibility
- Your environment already uses iSCSI infrastructure

**Comparison with other protocols:**

| Feature | iSCSI | NVMe-oF | NFS |
|---------|-------|---------|-----|
| **Type** | Block | Block | File |
| **Access Mode** | RWO | RWO | RWX |
| **Performance** | Good | Best | Good |
| **Setup Complexity** | Low | Medium | Low |
| **Node Requirements** | open-iscsi | nvme-cli, kernel modules | nfs-common |

## Prerequisites

### TrueNAS Requirements

- **TrueNAS Scale 25.10 or later**
- iSCSI service enabled (System > Services > iSCSI)
- Storage pool available for volume provisioning

### Kubernetes Node Requirements

- `open-iscsi` package installed on all nodes
- `iscsid` service running

**Debian/Ubuntu:**
```bash
sudo apt-get update
sudo apt-get install -y open-iscsi
sudo systemctl enable iscsid
sudo systemctl start iscsid
```

**RHEL/CentOS/Rocky:**
```bash
sudo dnf install -y iscsi-initiator-utils
sudo systemctl enable iscsid
sudo systemctl start iscsid
```

**Verify installation:**
```bash
# Check iscsid is running
sudo systemctl status iscsid

# Check initiator name exists
cat /etc/iscsi/initiatorname.iscsi
```

## TrueNAS Setup

### Step 1: Enable iSCSI Service

1. Navigate to **System > Services**
2. Find **iSCSI** in the list
3. Toggle the service **ON**
4. Check **Start Automatically** to enable on boot

That's it! Unlike NVMe-oF, iSCSI doesn't require pre-configured portals or targets. The CSI driver automatically manages:

- iSCSI targets (one per volume)
- iSCSI extents (ZVOL-backed)
- Target-extent associations
- Initiator groups

### Architecture

```
+-------------------------------------------------------------+
|                    TrueNAS iSCSI                             |
+-------------------------------------------------------------+
|  Target (per volume)      <- CSI driver creates/deletes     |
|    +-- Extent (ZVOL)      <- CSI driver creates/deletes     |
|    +-- Target-Extent      <- CSI driver creates/deletes     |
|    +-- Initiator Group    <- CSI driver creates/deletes     |
+-------------------------------------------------------------+
```

## Installation

### Quick Install with Helm

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.iscsi.enabled=true \
  --set storageClasses.iscsi.pool="YOUR-POOL-NAME" \
  --set storageClasses.iscsi.server="YOUR-TRUENAS-IP"
```

**Replace these values:**
- `YOUR-TRUENAS-IP` - Your TrueNAS server IP address
- `YOUR-API-KEY` - API key from TrueNAS (Settings > API Keys)
- `YOUR-POOL-NAME` - ZFS pool name (e.g., `tank`, `storage`)

### Verify Installation

```bash
# Check pods are running
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# Check storage class was created
kubectl get storageclass tns-csi-iscsi

# View controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver
```

## Usage

### Create a PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-iscsi-volume
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: tns-csi-iscsi
```

Apply:
```bash
kubectl apply -f my-pvc.yaml
kubectl get pvc my-iscsi-volume
```

### Use in a Pod

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
      claimName: my-iscsi-volume
```

### Use Raw Block Device

iSCSI volumes can be used as raw block devices:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-block-volume
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Block
  resources:
    requests:
      storage: 10Gi
  storageClassName: tns-csi-iscsi
---
apiVersion: v1
kind: Pod
metadata:
  name: block-app
spec:
  containers:
  - name: app
    image: ubuntu:latest
    command: ["sleep", "infinity"]
    volumeDevices:
    - name: data
      devicePath: /dev/xvda
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-block-volume
```

### Test I/O

```bash
# Create pod
kubectl apply -f my-pod.yaml

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/my-app --timeout=120s

# Write test data
kubectl exec my-app -- sh -c "echo 'Hello iSCSI' > /data/test.txt && sync"

# Read back
kubectl exec my-app -- cat /data/test.txt

# Test larger I/O
kubectl exec my-app -- dd if=/dev/zero of=/data/testfile bs=1M count=100
```

## Advanced Configuration

### Custom Storage Class

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: iscsi-fast
provisioner: tns.csi.io
parameters:
  protocol: iscsi
  server: YOUR-TRUENAS-IP
  pool: tank
  port: "3260"
  csi.storage.k8s.io/fstype: ext4
  # ZFS properties
  zfs.compression: lz4
  zfs.volblocksize: 16K
allowVolumeExpansion: true
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

### Volume Retention

To keep volumes on TrueNAS when PVCs are deleted:

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.iscsi.enabled=true \
  --set storageClasses.iscsi.pool="YOUR-POOL-NAME" \
  --set storageClasses.iscsi.server="YOUR-TRUENAS-IP" \
  --set "storageClasses.iscsi.parameters.deleteStrategy=retain"
```

### Encryption

Enable ZFS native encryption for iSCSI volumes:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: iscsi-encrypted
provisioner: tns.csi.io
parameters:
  protocol: iscsi
  server: YOUR-TRUENAS-IP
  pool: tank
  port: "3260"
  csi.storage.k8s.io/fstype: ext4
  encryption: "true"
  encryptionGenerateKey: "true"
allowVolumeExpansion: true
```

## Snapshots

### Create Snapshot Class

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: tns-csi-iscsi-snapclass
driver: tns.csi.io
deletionPolicy: Delete
```

### Create Snapshot

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-snapshot
spec:
  volumeSnapshotClassName: tns-csi-iscsi-snapclass
  source:
    persistentVolumeClaimName: my-iscsi-volume
```

### Restore from Snapshot

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-volume
spec:
  storageClassName: tns-csi-iscsi
  dataSource:
    name: my-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

## Troubleshooting

### Check Driver Status

```bash
# Check all pods are running
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# View controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver --tail=50

# View node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver --tail=50
```

### PVC Stuck in Pending

```bash
# Describe PVC for events
kubectl describe pvc my-iscsi-volume

# Check controller logs for errors
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver | grep -i error
```

### Pod Stuck in ContainerCreating

```bash
# Describe pod for events
kubectl describe pod my-app

# Check node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver | grep -i error

# Check iscsid is running on the node
ssh <node> sudo systemctl status iscsid
```

### Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| PVC pending | iSCSI service not enabled | Enable iSCSI in TrueNAS (System > Services) |
| Mount failed | iscsid not running | `sudo systemctl start iscsid` on nodes |
| Connection refused | Firewall blocking port 3260 | Open port 3260 between nodes and TrueNAS |
| Authentication failed | Initiator not recognized | Check initiator name in `/etc/iscsi/initiatorname.iscsi` |

### Verify Connectivity

From a Kubernetes node:

```bash
# Check port is open
nc -zv YOUR-TRUENAS-IP 3260

# Discover targets (after volume is created)
sudo iscsiadm -m discovery -t sendtargets -p YOUR-TRUENAS-IP:3260

# List active sessions
sudo iscsiadm -m session
```

## Performance Tuning

### Recommended Settings

For optimal performance, consider these settings in your StorageClass:

```yaml
parameters:
  # Use 16K block size for general workloads
  zfs.volblocksize: "16K"
  # Enable compression for storage efficiency
  zfs.compression: "lz4"
  # Use ext4 or xfs for filesystem
  csi.storage.k8s.io/fstype: "ext4"
```

### Database Workloads

For databases (PostgreSQL, MySQL, etc.):

```yaml
parameters:
  zfs.volblocksize: "8K"
  zfs.compression: "off"
  zfs.sync: "standard"
  csi.storage.k8s.io/fstype: "xfs"
```

## Next Steps

- [Snapshots Guide](SNAPSHOTS.md) - Full snapshot and cloning documentation
- [Features Documentation](FEATURES.md) - Complete feature reference
- [Deployment Guide](DEPLOYMENT.md) - Production deployment best practices
- [Metrics Guide](METRICS.md) - Monitoring with Prometheus
