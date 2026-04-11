# Quick Start: SMB/CIFS File Storage

**WARNING: EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

This guide explains how to set up and use SMB/CIFS file storage with the TrueNAS CSI driver.

## Overview

SMB (Server Message Block) provides network file sharing with authentication support. It's a good choice when:

- You need shared file storage with user-level access control
- Your environment requires Windows-compatible file sharing
- You need authenticated access to volumes (username/password)

**Comparison with other protocols:**

| Feature | SMB | NFS | iSCSI | NVMe-oF |
|---------|-----|-----|-------|---------|
| **Type** | File | File | Block | Block |
| **Access Mode** | RWX | RWX | RWO | RWO |
| **Performance** | Good | Good | Good | Best |
| **Authentication** | Username/password | Host-based | Initiator-based | None |
| **Setup Complexity** | Medium | Low | Low | Medium |
| **Node Requirements** | cifs-utils | nfs-common | open-iscsi | nvme-cli |

## Prerequisites

### TrueNAS Requirements

- **TrueNAS Scale 25.10 or later**
- SMB service enabled (System > Services > SMB)
- Storage pool available for volume provisioning
- SMB user account created (Credentials > Local Users)

### Kubernetes Node Requirements

- `cifs-utils` package installed on all nodes

**Debian/Ubuntu:**
```bash
sudo apt-get update
sudo apt-get install -y cifs-utils
```

**RHEL/CentOS/Rocky:**
```bash
sudo dnf install -y cifs-utils
```

**Verify installation:**
```bash
# Check mount.cifs is available
which mount.cifs
```

## TrueNAS Setup

### Step 1: Enable SMB Service

1. Navigate to **System > Services**
2. Find **SMB** in the list
3. Toggle the service **ON**
4. Check **Start Automatically** to enable on boot

### Step 2: Create an SMB User

The CSI driver uses SMB credentials to mount shares on Kubernetes nodes:

1. Navigate to **Credentials > Local Users**
2. Click **Add**
3. Configure:
   - **Username**: `csi-smb` (or any name)
   - **Password**: Set a strong password
   - **SMB Authentication**: Ensure **Samba Authentication** is enabled
4. Click **Save**

The CSI driver automatically creates and deletes SMB shares for each volume. You only need to provide credentials for mounting.

### Architecture

```
+-------------------------------------------------------------+
|                    TrueNAS SMB                               |
+-------------------------------------------------------------+
|  Dataset (per volume)     <- CSI driver creates/deletes     |
|    +-- SMB Share          <- CSI driver creates/deletes     |
+-------------------------------------------------------------+
```

## Installation

### Step 1: Create SMB Credentials Secret

Create a Kubernetes Secret with your SMB credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: smb-credentials
  namespace: kube-system
type: Opaque
stringData:
  username: "csi-smb"
  password: "your-password"
  # domain: "WORKGROUP"  # Optional, defaults to WORKGROUP
```

Apply:
```bash
kubectl apply -f smb-credentials.yaml
```

### Step 2: Install with Helm

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.17.5 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="tns-csi-smb" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="smb" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP" \
  --set storageClasses[0].smbCredentialsSecret.name="smb-credentials" \
  --set storageClasses[0].smbCredentialsSecret.namespace="kube-system"
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
kubectl get storageclass tns-csi-smb

# View controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver
```

## Usage

### Create a PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-smb-volume
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
  storageClassName: tns-csi-smb
```

Apply:
```bash
kubectl apply -f my-pvc.yaml
kubectl get pvc my-smb-volume
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
      claimName: my-smb-volume
```

### Test I/O

```bash
# Create pod
kubectl apply -f my-pod.yaml

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/my-app --timeout=120s

# Write test data
kubectl exec my-app -- sh -c "echo 'Hello SMB' > /data/test.txt && sync"

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
  name: smb-custom
provisioner: tns.csi.io
parameters:
  protocol: smb
  server: YOUR-TRUENAS-IP
  pool: tank
  csi.storage.k8s.io/node-stage-secret-name: smb-credentials
  csi.storage.k8s.io/node-stage-secret-namespace: kube-system
  # ZFS properties
  zfs.compression: lz4
allowVolumeExpansion: true
reclaimPolicy: Delete
volumeBindingMode: Immediate
mountOptions:
  - vers=3.0
  - seal       # Enable SMB3 encryption in transit
```

### Volume Retention

To keep volumes on TrueNAS when PVCs are deleted:

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.17.5 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="tns-csi-smb" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="smb" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-TRUENAS-IP" \
  --set storageClasses[0].smbCredentialsSecret.name="smb-credentials" \
  --set storageClasses[0].smbCredentialsSecret.namespace="kube-system" \
  --set "storageClasses[0].parameters.deleteStrategy=retain"
```

### Encryption

Enable ZFS native encryption for SMB volumes:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: smb-encrypted
provisioner: tns.csi.io
parameters:
  protocol: smb
  server: YOUR-TRUENAS-IP
  pool: tank
  csi.storage.k8s.io/node-stage-secret-name: smb-credentials
  csi.storage.k8s.io/node-stage-secret-namespace: kube-system
  encryption: "true"
  encryptionGenerateKey: "true"
allowVolumeExpansion: true
```

### Mount Options

SMB mount options can be customized via the StorageClass:

| Option | Description |
|--------|-------------|
| `vers=3.0` | SMB protocol version (default) |
| `file_mode=0777` | File permission mask (default) |
| `dir_mode=0777` | Directory permission mask (default) |
| `seal` | Enable SMB3 encryption in transit |
| `cache=strict` | Strict caching for data consistency |
| `uid=1000` | Map files to specific UID |
| `gid=1000` | Map files to specific GID |

## Snapshots

### Create Snapshot Class

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: tns-csi-smb-snapclass
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
  volumeSnapshotClassName: tns-csi-smb-snapclass
  source:
    persistentVolumeClaimName: my-smb-volume
```

### Restore from Snapshot

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-volume
spec:
  storageClassName: tns-csi-smb
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
kubectl describe pvc my-smb-volume

# Check controller logs for errors
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver | grep -i error
```

### Pod Stuck in ContainerCreating

```bash
# Describe pod for events
kubectl describe pod my-app

# Check node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-driver | grep -i error

# Check cifs-utils is installed on the node
ssh <node> which mount.cifs
```

### Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| PVC pending | SMB service not enabled | Enable SMB in TrueNAS (System > Services) |
| Mount failed | cifs-utils not installed | `sudo apt-get install cifs-utils` on nodes |
| Permission denied | Wrong credentials | Verify Secret contents and SMB user in TrueNAS |
| Connection refused | Firewall blocking port 445 | Open port 445 between nodes and TrueNAS |
| Share not found | Share creation failed | Check controller logs for TrueNAS API errors |

### Verify Connectivity

From a Kubernetes node:

```bash
# Check port is open
nc -zv YOUR-TRUENAS-IP 445

# Test SMB access (requires smbclient)
smbclient -L //YOUR-TRUENAS-IP -U csi-smb
```

## Next Steps

- [Snapshots Guide](SNAPSHOTS.md) - Full snapshot and cloning documentation
- [Features Documentation](FEATURES.md) - Complete feature reference
- [Deployment Guide](DEPLOYMENT.md) - Production deployment best practices
- [Metrics Guide](METRICS.md) - Monitoring with Prometheus
