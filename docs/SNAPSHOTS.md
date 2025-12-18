# Volume Snapshots Guide

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Snapshot functionality is implemented but requires thorough testing. Use only for testing and evaluation.

This guide explains how to use volume snapshots with the TrueNAS CSI driver.

## Overview

The TrueNAS CSI driver supports creating, deleting, and restoring from volume snapshots for both NFS and NVMe-oF protocols. Snapshots leverage ZFS snapshot capabilities on TrueNAS, providing instant, space-efficient point-in-time copies of your data.

## Features (Implementation Status)

- ✅ **Create snapshots** - Implemented for both NFS and NVMe-oF (testing in progress)
- ✅ **Delete snapshots** - Implemented for both protocols (testing in progress)
- ✅ **List snapshots** - Implemented (testing in progress)
- ✅ **Restore from snapshots** - Create new volumes from snapshots via cloning (testing in progress)
- ✅ **NFS support** - Snapshot operations implemented (validation needed)
- ✅ **NVMe-oF support** - Snapshot operations implemented (validation needed)
- ✅ **Idempotent operations** - Safe to retry create/delete operations
- ✅ **Detached snapshots** - Independent dataset copies via zfs send/receive that survive source volume deletion

**Note:** While snapshot functionality is implemented, it requires comprehensive testing before production use.

## Prerequisites

### Required Components

1. **Kubernetes Snapshot CRDs** (v1 API):
   ```bash
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v6.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v6.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v6.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
   ```

2. **Snapshot Controller**:
   ```bash
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v6.3.0/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
   kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v6.3.0/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
   ```

3. **CSI Snapshotter Sidecar** - Already included in the TrueNAS CSI driver Helm chart

### Verify Prerequisites

```bash
# Check CRDs are installed
kubectl get crd | grep volumesnapshot

# Expected output:
# volumesnapshotclasses.snapshot.storage.k8s.io
# volumesnapshotcontents.snapshot.storage.k8s.io
# volumesnapshots.snapshot.storage.k8s.io

# Check snapshot controller is running
kubectl get pods -n kube-system | grep snapshot-controller
```

## Quick Start

### 1. Create a VolumeSnapshotClass

Create a VolumeSnapshotClass for your storage protocol:

**For NFS:**
```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapclass
driver: tns.csi.io
deletionPolicy: Delete
```

**For NVMe-oF:**
```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nvmeof-snapclass
driver: tns.csi.io
deletionPolicy: Delete
```

Apply it:
```bash
kubectl apply -f volumesnapshotclass.yaml
```

### 2. Create a Volume Snapshot

Create a snapshot of an existing PVC:

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-snapshot
  namespace: default
spec:
  volumeSnapshotClassName: truenas-nfs-snapclass
  source:
    persistentVolumeClaimName: my-pvc
```

Apply it:
```bash
kubectl apply -f volumesnapshot.yaml
```

### 3. Check Snapshot Status

```bash
# Check snapshot status
kubectl get volumesnapshot my-snapshot

# Expected output:
# NAME          READYTOUSE   SOURCEPVC   SOURCESNAPSHOTCONTENT   RESTORESIZE   SNAPSHOTCLASS              SNAPSHOTCONTENT                                    CREATIONTIME   AGE
# my-snapshot   true         my-pvc                              10Gi          truenas-nfs-snapclass      snapcontent-xxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx      5s             5s

# Get detailed information
kubectl describe volumesnapshot my-snapshot
```

### 4. Restore from Snapshot

Create a new PVC from the snapshot:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-pvc
  namespace: default
spec:
  storageClassName: truenas-nfs  # Must match original PVC's storage class
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

Apply it:
```bash
kubectl apply -f restored-pvc.yaml
```

The new PVC will be provisioned as a ZFS clone of the snapshot, containing all data from the snapshot point.

## Complete Example Workflow

Here's a complete example demonstrating the snapshot workflow:

```bash
# 1. Create a PVC with some data
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: source-pvc
spec:
  storageClassName: truenas-nfs
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 5Gi
EOF

# 2. Create a pod to write data
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: writer-pod
spec:
  containers:
  - name: writer
    image: busybox
    command: ['sh', '-c', 'echo "Important data at $(date)" > /data/important.txt && sleep 3600']
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: source-pvc
EOF

# 3. Wait for pod to write data
sleep 10

# 4. Verify data was written
kubectl exec writer-pod -- cat /data/important.txt

# 5. Create VolumeSnapshotClass
kubectl apply -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapclass
driver: tns.csi.io
deletionPolicy: Delete
EOF

# 6. Create a snapshot
kubectl apply -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: backup-snapshot
spec:
  volumeSnapshotClassName: truenas-nfs-snapclass
  source:
    persistentVolumeClaimName: source-pvc
EOF

# 7. Wait for snapshot to be ready
kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/backup-snapshot --timeout=60s

# 8. Create a new PVC from the snapshot
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-pvc
spec:
  storageClassName: truenas-nfs
  dataSource:
    name: backup-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 5Gi
EOF

# 9. Create a pod to read restored data
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: reader-pod
spec:
  containers:
  - name: reader
    image: busybox
    command: ['sh', '-c', 'cat /data/important.txt && sleep 3600']
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: restored-pvc
EOF

# 10. Verify restored data matches original
kubectl logs reader-pod
```

## NVMe-oF Snapshot Example

The process is identical for NVMe-oF volumes, just use the NVMe-oF storage class and snapshot class:

```yaml
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nvmeof-snapclass
driver: tns.csi.io
deletionPolicy: Delete
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: nvmeof-snapshot
spec:
  volumeSnapshotClassName: truenas-nvmeof-snapclass
  source:
    persistentVolumeClaimName: nvmeof-pvc
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-nvmeof-pvc
spec:
  storageClassName: truenas-nvmeof
  dataSource:
    name: nvmeof-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

## Advanced Usage

### Snapshot Deletion Policy

The `deletionPolicy` field in VolumeSnapshotClass controls what happens when a VolumeSnapshot is deleted:

- **Delete** (default): Snapshot is deleted from TrueNAS when VolumeSnapshot is deleted
- **Retain**: Snapshot is kept on TrueNAS even after VolumeSnapshot is deleted

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-retain
driver: tns.csi.io
deletionPolicy: Retain  # Keep snapshots on TrueNAS
```

### Pre-Provisioned Snapshots

You can also import existing ZFS snapshots into Kubernetes (advanced use case):

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotContent
metadata:
  name: pre-existing-snapshot
spec:
  deletionPolicy: Retain
  driver: tns.csi.io
  source:
    snapshotHandle: <base64-encoded-snapshot-metadata>
  volumeSnapshotRef:
    name: imported-snapshot
    namespace: default
```

## Troubleshooting

### Snapshot Stuck in Pending

**Check snapshot controller logs:**
```bash
kubectl logs -n kube-system -l app=snapshot-controller
```

**Check CSI controller logs:**
```bash
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-driver
```

**Common causes:**
- Snapshot CRDs not installed
- Snapshot controller not running
- Source PVC doesn't exist
- TrueNAS API connection issues

### Restore from Snapshot Fails

**Check events:**
```bash
kubectl describe pvc restored-pvc
```

**Common causes:**
- VolumeSnapshot not ready (check with `kubectl get volumesnapshot`)
- StorageClass doesn't match original PVC
- Insufficient space on TrueNAS
- Protocol mismatch (trying to restore NFS snapshot to NVMe-oF PVC)

### Snapshot Not Deleted from TrueNAS

**Check if VolumeSnapshotContent still exists:**
```bash
kubectl get volumesnapshotcontent
```

**Force delete if necessary:**
```bash
kubectl delete volumesnapshotcontent <content-name> --force --grace-period=0
```

**Verify on TrueNAS:**
```bash
# SSH into TrueNAS or use the UI to check:
# Storage > Snapshots
# Look for snapshots with names like: tank/k8s-volumes/pvc-xxxxx@snapshot-name
```

## How It Works

### Snapshot Creation

1. User creates a VolumeSnapshot resource
2. Snapshot controller creates VolumeSnapshotContent
3. CSI external-snapshotter sidecar calls `CreateSnapshot` RPC
4. TrueNAS CSI driver calls TrueNAS API: `zfs.snapshot.create`
5. ZFS creates instant snapshot (copy-on-write, no data duplication)
6. Driver returns snapshot metadata (encoded in snapshot ID)
7. VolumeSnapshot becomes `ReadyToUse: true`

### Snapshot Restoration (Cloning)

1. User creates PVC with `dataSource` pointing to VolumeSnapshot
2. CSI external-provisioner detects snapshot dataSource
3. Driver's `CreateVolume` is called with snapshot parameter
4. Driver decodes snapshot metadata to get ZFS snapshot name
5. Driver calls TrueNAS API: `zfs.snapshot.clone`
6. ZFS creates clone (instant, copy-on-write)
7. For NFS: Driver creates NFS share for the clone
8. For NVMe-oF: Driver creates namespace and target for the clone
9. Volume is provisioned and ready to use

### Snapshot Deletion

1. User deletes VolumeSnapshot resource
2. Snapshot controller handles VolumeSnapshotContent deletion
3. CSI external-snapshotter calls `DeleteSnapshot` RPC
4. Driver calls TrueNAS API: `zfs.snapshot.delete`
5. ZFS removes snapshot (space reclaimed based on references)
6. VolumeSnapshotContent is removed

## Performance Considerations

### Snapshot Creation

- **Near-instant**: ZFS snapshots are created instantly regardless of volume size
- **Space-efficient**: No data is copied during snapshot creation
- **Minimal overhead**: Snapshots use copy-on-write, only changed blocks consume space

### Cloning from Snapshots

- **Instant clone creation**: ZFS clones are created instantly
- **Space-efficient**: Clones share data with the original until modified
- **Performance**: Clones have same performance as regular volumes

### Storage Impact

- **Shared blocks**: Snapshots and clones share blocks with the original
- **Space usage**: Only grows as data diverges from snapshot
- **Cleanup**: Deleting snapshots may not immediately free space if clones exist

## Best Practices

### 1. Regular Snapshots for Backup

Create snapshots before risky operations:
```bash
# Before upgrading an application
kubectl apply -f pre-upgrade-snapshot.yaml

# Upgrade application
helm upgrade my-app ...

# If upgrade fails, restore from snapshot
kubectl apply -f restore-from-snapshot-pvc.yaml
```

### 2. Automate Snapshots with CronJobs

Create periodic snapshots:
```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: daily-snapshot
spec:
  schedule: "0 2 * * *"  # 2 AM daily
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: snapshot-creator
          containers:
          - name: create-snapshot
            image: bitnami/kubectl:latest
            command:
            - /bin/sh
            - -c
            - |
              kubectl create -f - <<EOF
              apiVersion: snapshot.storage.k8s.io/v1
              kind: VolumeSnapshot
              metadata:
                name: daily-backup-$(date +%Y%m%d-%H%M%S)
              spec:
                volumeSnapshotClassName: truenas-nfs-snapclass
                source:
                  persistentVolumeClaimName: production-data
              EOF
          restartPolicy: OnFailure
```

### 3. Snapshot Naming Convention

Use descriptive names:
```yaml
metadata:
  name: myapp-pre-upgrade-v2.0-20250105
```

### 4. Clean Up Old Snapshots

Delete snapshots that are no longer needed:
```bash
# List snapshots older than 30 days
kubectl get volumesnapshot -o json | jq -r '.items[] | select(.metadata.creationTimestamp | fromdateiso8601 < (now - 30*86400)) | .metadata.name'

# Delete old snapshots
kubectl delete volumesnapshot snapshot-20241201
```

### 5. Test Restore Procedures

Regularly test restoring from snapshots to ensure they work:
```bash
# 1. Create snapshot
# 2. Restore to new PVC
# 3. Verify data integrity
# 4. Document the process
```

## Security Considerations

### RBAC for Snapshots

Create appropriate RBAC rules:
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: snapshot-user
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: snapshot-user
  namespace: default
rules:
- apiGroups: ["snapshot.storage.k8s.io"]
  resources: ["volumesnapshots"]
  verbs: ["get", "list", "create", "delete"]
- apiGroups: [""]
  resources: ["persistentvolumeclaims"]
  verbs: ["get", "list", "create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: snapshot-user
  namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: snapshot-user
subjects:
- kind: ServiceAccount
  name: snapshot-user
  namespace: default
```

### Snapshot Encryption

Snapshots inherit the encryption settings of the parent ZFS dataset. If your TrueNAS pool uses ZFS encryption, snapshots are automatically encrypted.

## Detached Snapshots

The TrueNAS CSI driver supports two types of detached operations:

1. **Detached Clones** (via clone promotion) - When restoring a volume from a snapshot
2. **Detached Snapshots** (via zfs send/receive) - When creating a snapshot that survives source volume deletion

### Detached Clones (Independent Volume Restoration)

By default, when you restore a volume from a snapshot, the new volume is a ZFS clone that depends on the parent snapshot. This means:
- The snapshot cannot be deleted while the clone exists
- Deleting the snapshot would fail or orphan the clone

**Detached clones** solve this by creating a **promoted clone** that is completely independent from the source snapshot. After promotion, the clone has no dependency on the original snapshot, which can then be safely deleted.

#### When to Use Detached Clones

- **Snapshot rotation**: You want to create clones but also rotate/delete old snapshots
- **Independent copies**: You need a fully independent copy of data that outlives the snapshot
- **Cleanup flexibility**: You want the freedom to delete snapshots without worrying about clone dependencies

#### How to Use Detached Clones

1. **Create a StorageClass with `detached: "true"`**:

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
  # Enable detached clones - clones will be promoted to be independent
  detached: "true"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

2. **Create a PVC from a snapshot using the detached StorageClass**:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: detached-clone-pvc
spec:
  storageClassName: truenas-nfs-detached  # Uses detached StorageClass
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

3. **Now you can delete the original snapshot** without affecting the cloned volume:

```bash
# This works because the clone is independent (promoted)
kubectl delete volumesnapshot my-snapshot
```

### Detached Snapshots (Survive Source Volume Deletion)

**Detached snapshots** are a more advanced feature that creates completely independent dataset copies using `zfs send | zfs receive`. Unlike regular ZFS snapshots (which are COW and depend on the source volume), detached snapshots:

- **Survive source volume deletion** - The snapshot remains even if you delete the original PVC
- **Are stored as separate datasets** - Located in a dedicated parent dataset (e.g., `tank/csi-detached-snapshots/`)
- **Use full data copy** - Data is copied via zfs send/receive, not copy-on-write

#### When to Use Detached Snapshots

- **Backup/DR scenarios**: You need snapshots that survive the deletion of source volumes
- **Data migration**: Moving data between volumes where the source will be deleted
- **Long-term archival**: Snapshots that must persist independently of the source lifecycle
- **Compliance requirements**: When regulations require independent backup copies

#### How to Use Detached Snapshots

**Option 1: Using Helm Chart (Recommended)**

Enable detached snapshots in your Helm values:

```yaml
snapshots:
  enabled: true
  detached:
    enabled: true
    # Optional: specify parent dataset for detached snapshots
    # If not set, defaults to {pool}/csi-detached-snapshots
    parentDataset: "tank/backups/csi-snapshots"
    deletionPolicy: Delete
```

This creates a VolumeSnapshotClass named `{storageclass}-snapshot-detached` with the appropriate parameters.

**Option 2: Manual VolumeSnapshotClass**

Create a VolumeSnapshotClass with `detachedSnapshots: "true"`:

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapshot-detached
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  # Enable detached snapshots via zfs send/receive
  detachedSnapshots: "true"
  # Optional: specify where detached snapshots are stored
  # Defaults to {pool}/csi-detached-snapshots if not specified
  detachedSnapshotsParentDataset: "tank/backups/csi-snapshots"
```

**Create a detached snapshot:**

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-detached-snapshot
spec:
  volumeSnapshotClassName: truenas-nfs-snapshot-detached
  source:
    persistentVolumeClaimName: my-pvc
```

**Now you can safely delete the source PVC:**

```bash
# Delete the source volume - the detached snapshot survives!
kubectl delete pvc my-pvc

# The snapshot is still available
kubectl get volumesnapshot my-detached-snapshot
# NAME                    READYTOUSE   ...
# my-detached-snapshot    true         ...
```

#### Detached Snapshots: How It Works

1. User creates VolumeSnapshot with `detachedSnapshots: "true"` VolumeSnapshotClass
2. Driver creates a temporary ZFS snapshot on the source volume
3. Driver runs `replication.run_onetime` with LOCAL transport (zfs send | zfs receive)
4. Data is copied to a new dataset under the detached snapshots parent folder
5. Temporary snapshots are cleaned up
6. Result: Independent dataset at `{pool}/csi-detached-snapshots/{snapshot-name}`

#### Detached Snapshots vs Regular Snapshots

| Aspect | Regular Snapshot | Detached Snapshot |
|--------|------------------|-------------------|
| **Storage mechanism** | ZFS COW snapshot | Full dataset copy via zfs send/receive |
| **Source dependency** | Depends on source volume | Completely independent |
| **Source deletion** | Snapshot deleted with source | Snapshot survives source deletion |
| **Creation speed** | Instant | Proportional to data size |
| **Storage efficiency** | Space-efficient (COW) | Full copy (uses more space) |
| **Use case** | Point-in-time recovery | Backup/DR, long-term archival |
| **Location** | Same dataset as source | Separate parent dataset |

### NVMe-oF Detached Operations

Both detached clones and detached snapshots work with NVMe-oF:

**Detached Clone StorageClass:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nvmeof-detached
provisioner: tns.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: truenas.local
  subsystemNQN: nqn.2025-01.com.truenas:csi
  detached: "true"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**Detached Snapshot VolumeSnapshotClass:**
```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nvmeof-snapshot-detached
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  detachedSnapshots: "true"
  detachedSnapshotsParentDataset: "tank/backups/nvmeof-snapshots"
```

## Limitations

- **Cross-protocol cloning**: Cannot restore NFS snapshot to NVMe-oF volume (or vice versa)
- **Size changes**: Restored PVC must be same size or larger than original
- **Cross-pool cloning**: Snapshots must be restored to the same ZFS pool
- **Namespace isolation**: Snapshots are namespace-scoped (cannot restore across namespaces without VolumeSnapshotContent)

## See Also

- [Kubernetes Volume Snapshots Documentation](https://kubernetes.io/docs/concepts/storage/volume-snapshots/)
- [CSI Snapshotter Documentation](https://github.com/kubernetes-csi/external-snapshotter)
- [ZFS Snapshot Documentation](https://openzfs.github.io/openzfs-docs/man/8/zfs-snapshot.8.html)
- [TrueNAS API Documentation](https://www.truenas.com/docs/api/)
