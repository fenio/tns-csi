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
