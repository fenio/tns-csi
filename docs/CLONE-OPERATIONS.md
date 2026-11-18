# Clone and Snapshot Operations Guide

This document explains the snapshot and clone operations available in the TrueNAS CSI driver, their underlying ZFS mechanisms, and when to use each approach.

## Table of Contents

- [ZFS Fundamentals](#zfs-fundamentals)
- [CSI Operations Overview](#csi-operations-overview)
- [Volume Clone Modes](#volume-clone-modes)
- [StorageClass Parameters](#storageclass-parameters)
- [VolumeSnapshotClass Parameters](#volumesnapshotclass-parameters)
- [Operation Matrix](#operation-matrix)
- [Decision Guide](#decision-guide)
- [Complete Examples](#complete-examples)

## ZFS Fundamentals

Understanding these ZFS concepts is essential for choosing the right operation:

### ZFS Snapshot

A **snapshot** is a read-only, point-in-time copy of a dataset.

```
pool/volume                    # Original dataset
pool/volume@snap-2025-01-24    # Snapshot (read-only)
```

**Characteristics:**
- Instant creation (no data copy)
- Space-efficient (Copy-on-Write - only stores changes)
- Read-only (cannot be modified)
- Depends on the source dataset (deleted if source is deleted)

### ZFS Clone

A **clone** is a writable copy created FROM a snapshot.

```
pool/volume@snap-2025-01-24    # Source snapshot
pool/clone-volume              # Clone (writable)
```

**Characteristics:**
- Instant creation (no data copy)
- Space-efficient (shares blocks with snapshot until modified)
- Writable (independent modifications)
- **Depends on the source snapshot** - the snapshot cannot be deleted while clones exist

### ZFS Promote

**Promote** reverses the parent-child relationship between a clone and its origin snapshot.

```
Before promotion:
  pool/volume@snap → pool/clone-volume (clone depends on snapshot)

After promotion:
  pool/clone-volume@snap ← pool/volume (original depends on promoted dataset)
```

**Characteristics:**
- Instant operation (no data copy)
- Reverses the dependency relationship
- Allows deleting the original snapshot/volume
- The promoted clone becomes the new "origin" - it cannot be deleted while dependents exist

### ZFS Send/Receive

**Send/receive** creates a completely independent copy of a dataset by streaming the data.

```bash
zfs send pool/source@snap | zfs receive pool/target
```

**Characteristics:**
- Full data copy (not instant)
- Completely independent (no shared blocks)
- No dependency on source in either direction
- Uses more storage space

## CSI Operations Overview

The CSI spec defines two ways to create volumes from existing data:

### 1. Create Volume from Snapshot (VolumeSnapshot source)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
spec:
  dataSource:
    kind: VolumeSnapshot
    name: my-snapshot
    apiGroup: snapshot.storage.k8s.io
```

This restores data from a VolumeSnapshot to a new PVC.

### 2. Create Volume from Volume (PVC source)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
spec:
  dataSource:
    kind: PersistentVolumeClaim
    name: source-pvc
```

This clones an existing PVC to a new PVC. Internally, a temporary snapshot is created, and the new volume is created from it.

## Volume Clone Modes

The TrueNAS CSI driver supports three clone modes, controlled by StorageClass parameters:

### 1. COW Clone (Default)

Standard ZFS clone that shares blocks with the source snapshot.

**Dependency:** Clone depends on snapshot → **snapshot cannot be deleted while clone exists**

```
Snapshot ← Clone
   │
   └── Snapshot cannot be deleted while clone exists
       Clone CAN be deleted anytime
```

**Characteristics:**
- Instant creation
- Maximum space efficiency (shares all blocks initially)
- Snapshot protected from deletion
- Clone can be freely deleted

**Use when:**
- Space efficiency is paramount
- Source snapshot/volume will persist
- You need instant clone creation

### 2. Promoted Clone (`promotedVolumesFromSnapshots` / `promotedVolumesFromVolumes`)

ZFS clone + promote. Creates a clone and then promotes it, reversing the dependency.

**Dependency:** After promotion, source depends on clone → **clone cannot be deleted while source exists**

```
Before: Snapshot ← Clone
After:  Snapshot → Clone (promoted)
           │
           └── Clone cannot be deleted while snapshot exists
               Snapshot CAN be deleted anytime
```

**Characteristics:**
- Instant creation
- Space efficient (shares blocks initially)
- Allows deleting the original snapshot
- Clone protected from deletion (while source exists)

**Use when:**
- You need snapshot rotation (delete old snapshots)
- Source volume/snapshot may be deleted
- Clone should persist as the "primary" copy

### 3. Detached Clone (`detachedVolumesFromSnapshots` / `detachedVolumesFromVolumes`)

Full data copy via zfs send/receive. Creates a completely independent volume.

**Dependency:** None → **both source and clone can be deleted in any order**

```
Snapshot    Clone (independent)
   │           │
   └── No dependency whatsoever
       Both can be deleted independently
```

**Characteristics:**
- Full data copy (slower, depends on data size)
- Complete independence
- Uses more storage space
- Both source and clone freely deletable

**Use when:**
- Complete independence is required
- Data migration scenarios
- Cross-pool copies
- Compliance requirements

## StorageClass Parameters

These parameters control clone behavior when creating volumes from snapshots or other volumes:

### For Restoring from Snapshots

| Parameter | Value | Behavior |
|-----------|-------|----------|
| (none) | - | COW clone (default) - clone depends on snapshot |
| `promotedVolumesFromSnapshots` | `"true"` | Clone + promote - snapshot depends on clone |
| `detachedVolumesFromSnapshots` | `"true"` | Send/receive - completely independent |

### For Cloning from Volumes

| Parameter | Value | Behavior |
|-----------|-------|----------|
| (none) | - | COW clone with temp snapshot (temp snapshot kept) |
| `promotedVolumesFromVolumes` | `"true"` | Clone + promote (temp snapshot deleted after) |
| `detachedVolumesFromVolumes` | `"true"` | Send/receive (temp snapshot deleted after) |

**Note:** If both `promoted*` and `detached*` are set, `detached*` takes precedence.

## VolumeSnapshotClass Parameters

### `detachedSnapshots`

Controls how snapshots are created.

| Value | Behavior | ZFS Operation |
|-------|----------|---------------|
| `"false"` (default) | Standard COW snapshot | `zfs snapshot` |
| `"true"` | Independent dataset copy | `zfs send/receive` via replication |

**Key difference:** Detached snapshots are stored as independent datasets (not ZFS snapshots) and survive the deletion of the source volume. A regular snapshot lives on its source dataset, so the driver does not delete a volume that still has CSI snapshots: `DeleteVolume` returns `FailedPrecondition`, the PV stays `Released` (with its data on TrueNAS) while csi-provisioner retries, and the volume is deleted once its VolumeSnapshots are removed.

## Operation Matrix

### Snapshot Creation

| Operation | Parameter | ZFS Operation | Dependency |
|-----------|-----------|---------------|------------|
| Regular Snapshot | `detachedSnapshots: "false"` | `zfs snapshot` | Snapshot depends on source volume |
| Detached Snapshot | `detachedSnapshots: "true"` | `zfs send/receive` | Independent (survives source deletion) |

### Volume from Snapshot

| Mode | Parameter | ZFS Operation | Dependency |
|------|-----------|---------------|------------|
| COW Clone | (default) | `zfs clone` | Clone depends on snapshot |
| Promoted Clone | `promotedVolumesFromSnapshots: "true"` | `zfs clone` + `zfs promote` | Snapshot depends on clone |
| Detached Clone | `detachedVolumesFromSnapshots: "true"` | `zfs send/receive` | Independent |

### Volume from Volume

| Mode | Parameter | ZFS Operation | Temp Snapshot |
|------|-----------|---------------|---------------|
| COW Clone | (default) | `zfs snapshot` + `zfs clone` | Kept (clone depends on it) |
| Promoted Clone | `promotedVolumesFromVolumes: "true"` | `zfs snapshot` + `zfs clone` + `zfs promote` | Deleted after promote |
| Detached Clone | `detachedVolumesFromVolumes: "true"` | `zfs snapshot` + `zfs send/receive` | Deleted after send/receive |

## Decision Guide

### Choose COW Clone (Default) when:
- Space efficiency is paramount
- Source will persist (won't be deleted)
- Instant creation is needed
- Creating test/dev copies from production

### Choose Promoted Clone when:
- You need to delete the source snapshot later
- Implementing snapshot rotation policies
- Clone becomes the "primary" copy
- Source is temporary

### Choose Detached Clone when:
- Complete independence required
- Cross-pool migrations
- Compliance/audit requirements for independent copies
- Source will definitely be deleted
- Storage efficiency is not critical

### Understanding Cleanup Order

With **COW clones** (default):
1. Delete clone first
2. Then delete snapshot (now unblocked)

With **Promoted clones**:
1. Delete snapshot first (now allowed)
2. Clone becomes independent (can delete anytime)

With **Detached clones**:
1. Delete either in any order
2. No dependency constraints

## Complete Examples

### StorageClass for Standard Volumes

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
# Default: COW clones for space efficiency
```

### StorageClass with Promoted Clones

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-promoted
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: truenas.local
  promotedVolumesFromSnapshots: "true"  # Allows snapshot deletion
  promotedVolumesFromVolumes: "true"    # Cleans up temp snapshots
```

### StorageClass with Detached (Independent) Clones

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
  detachedVolumesFromSnapshots: "true"  # Full independence via send/receive
  detachedVolumesFromVolumes: "true"    # Full independence via send/receive
```

### VolumeSnapshotClass for Regular Snapshots

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapshot
driver: tns.csi.io
deletionPolicy: Delete
# Default: Regular COW snapshots (depend on source volume)
```

### VolumeSnapshotClass for Detached Snapshots (DR/Archival)

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: truenas-nfs-snapshot-detached
driver: tns.csi.io
deletionPolicy: Delete
parameters:
  detachedSnapshots: "true"  # Survives source volume deletion
  detachedSnapshotsParentDataset: "tank/backups"  # Optional: custom location
```

### Example: Clone with Snapshot Rotation

This example shows how to use promoted clones for snapshot rotation:

```yaml
# Create a snapshot
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: my-snapshot-v1
spec:
  volumeSnapshotClassName: truenas-nfs-snapshot
  source:
    persistentVolumeClaimName: my-data

---
# Restore to a new PVC using promoted mode
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-data-restored
spec:
  storageClassName: truenas-nfs-promoted  # Uses promoted clones
  dataSource:
    kind: VolumeSnapshot
    name: my-snapshot-v1
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi

# After restore completes, the original snapshot can be deleted
# (dependency is reversed - snapshot depends on restored clone)
```

## See Also

- [SNAPSHOTS.md](SNAPSHOTS.md) - Detailed snapshot usage guide
- [ZFS documentation](https://openzfs.github.io/openzfs-docs/)
