# Volume Adoption Guide

This guide covers importing existing TrueNAS volumes into tns-csi management and adopting them into Kubernetes clusters.

## Overview

**Adoption** is the process of taking an existing TrueNAS dataset/ZVOL and making it available as a Kubernetes PersistentVolume managed by tns-csi. This is useful for:

- **Migration from democratic-csi** - Move volumes to tns-csi without data loss
- **Disaster recovery** - Restore volumes to a new cluster after failure
- **Cluster recreation** - Re-attach volumes after rebuilding a cluster
- **Manual volume import** - Bring manually-created TrueNAS volumes into Kubernetes

## Important Safety Warnings

**READ THIS BEFORE PROCEEDING**

1. **Always back up critical data** before any migration - Use `pg_dump`, application-level backups, or ZFS snapshots
2. **Scale down workloads first** - Never migrate volumes while pods are using them
3. **Set Retain policy** - Prevent accidental deletion during migration
4. **Test with non-critical volumes first** - Verify the process works in your environment
5. **StatefulSet volumes require exact PVC names** - Plan carefully for stateful workloads
6. **Suspend GitOps reconciliation** - If using Flux/ArgoCD, suspend kustomizations before manual changes

### Operator-Managed Workloads (CloudNativePG, etc.)

**Do NOT attempt PVC adoption for database operators like CloudNativePG, Zalando PostgreSQL Operator, or similar.**

These operators manage their own PVC lifecycle and expect specific naming conventions (e.g., `postgres-1`, `postgres-2`, `postgres-3` for CNPG). Attempting to adopt PVCs with different names will cause:
- Cluster stuck in "unrecoverable" state
- Operators continuously trying to recreate pods with wrong volumes
- Data corruption risks

**Instead, use dump/restore:**

```bash
# 1. Create a backup pod with the old volume mounted
kubectl run pg-recovery --image=postgres:16 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"pg-recovery","image":"postgres:16",
    "command":["sleep","infinity"],
    "volumeMounts":[{"name":"data","mountPath":"/var/lib/postgresql/data"}]}],
    "volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"old-pvc-name"}}]}}'

# 2. Start postgres and dump data
kubectl exec -it pg-recovery -- bash
pg_ctl start -D /var/lib/postgresql/data/pgdata
pg_dumpall -U postgres > /tmp/backup.sql

# 3. Copy backup out
kubectl cp pg-recovery:/tmp/backup.sql ./backup.sql

# 4. Restore to new cluster
kubectl exec -i postgres-1 -n db -- psql -U postgres < backup.sql
```

### Finalizers Can Block Deletion

PVs and PVCs have finalizers (`kubernetes.io/pv-protection`, `kubernetes.io/pvc-protection`) that prevent deletion while in use. If a PV/PVC is stuck in `Terminating`:

```bash
# Check finalizers
kubectl get pv <pv-name> -o jsonpath='{.metadata.finalizers}'

# Remove finalizers (only after confirming no pod is using the volume!)
kubectl patch pv <pv-name> -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl patch pvc <pvc-name> -n <namespace> -p '{"metadata":{"finalizers":null}}' --type=merge
```

**Warning**: Only remove finalizers after confirming:
- No pods are mounting the volume
- Data is backed up or you're certain the PV data is safe

## Adoption Workflow Overview

The full adoption process involves both **TrueNAS-side** and **Kubernetes-side** steps:

```
┌─────────────────────────────────────────────────────────────────┐
│                    KUBERNETES SIDE                              │
│  1. Scale down workload (pods stop using volume)                │
│  2. Set PV reclaim policy to Retain                             │
│  3. Delete old PVC (PV becomes Released, data safe)             │
│  4. Delete old PV (optional cleanup)                            │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    TRUENAS SIDE (tns-csi)                       │
│  5. Import dataset into tns-csi (sets ZFS properties)           │
│  6. Generate PV/PVC manifests                                   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    KUBERNETES SIDE                              │
│  7. Apply new PV/PVC manifests                                  │
│  8. Scale up workload                                           │
└─────────────────────────────────────────────────────────────────┘
```

## Migration from Democratic-CSI

This is the most common adoption scenario. Follow these steps carefully.

### Prerequisites

- kubectl access to the cluster
- kubectl tns-csi plugin installed ([installation guide](KUBECTL-PLUGIN.md))
- TrueNAS credentials configured (plugin auto-discovers from installed driver)

### Step-by-Step Migration

#### Example Scenario

Migrating a volume used by qbittorrent StatefulSet:

```
PVC:       config-qbittorrent-0 (namespace: media)
PV:        pvc-2cf78549-3392-457e-9119-6a7be7da6707
Dataset:   storage/iscsi/v/pvc-2cf78549-3392-457e-9119-6a7be7da6707
Protocol:  iSCSI (democratic-csi)
```

#### Step 1: Scale Down Workload

**CRITICAL**: Stop all pods using the volume before proceeding.

```bash
# For StatefulSet
kubectl scale statefulset qbittorrent -n media --replicas=0

# For Deployment
kubectl scale deployment myapp -n media --replicas=0

# Verify pods are terminated
kubectl get pods -n media -l app=qbittorrent
```

Wait until all pods are terminated before continuing.

#### Step 2: Protect the PV from Deletion

Set the reclaim policy to Retain so the PV won't be deleted when the PVC is removed:

```bash
# Check current reclaim policy
kubectl get pv pvc-2cf78549-3392-457e-9119-6a7be7da6707 -o jsonpath='{.spec.persistentVolumeReclaimPolicy}'

# Set to Retain if not already
kubectl patch pv pvc-2cf78549-3392-457e-9119-6a7be7da6707 \
  -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
```

#### Step 3: Delete the Old PVC

This releases the PV but keeps the data safe (because of Retain policy):

```bash
kubectl delete pvc config-qbittorrent-0 -n media
```

The PV will now show status `Released`:

```bash
kubectl get pv pvc-2cf78549-3392-457e-9119-6a7be7da6707
# STATUS: Released
```

#### Step 4: Import Dataset into tns-csi

Now use the tns-csi plugin to mark the dataset as managed by tns-csi:

```bash
# Dry run first to see what will happen
kubectl tns-csi import storage/iscsi/v/pvc-2cf78549-3392-457e-9119-6a7be7da6707 \
  --protocol iscsi \
  --dry-run

# If everything looks good, run for real
kubectl tns-csi import storage/iscsi/v/pvc-2cf78549-3392-457e-9119-6a7be7da6707 \
  --protocol iscsi
```

This sets ZFS properties on the dataset:
- `tns-csi:managed_by` = "tns-csi"
- `tns-csi:protocol` = "iscsi"
- `tns-csi:adoptable` = "true"
- And other metadata properties

#### Step 5: Generate New PV/PVC Manifests

Generate Kubernetes manifests for the tns-csi managed volume:

```bash
kubectl tns-csi adopt storage/iscsi/v/pvc-2cf78549-3392-457e-9119-6a7be7da6707 \
  --pvc-name config-qbittorrent-0 \
  --namespace media \
  --storage-class tns-iscsi \
  -o yaml > qbittorrent-volume.yaml
```

**Important for StatefulSets**: The `--pvc-name` must match the expected PVC name pattern: `<volumeClaimTemplate-name>-<statefulset-name>-<ordinal>`

Review the generated manifests:

```bash
cat qbittorrent-volume.yaml
```

#### Step 6: Delete the Old PV (Cleanup)

The old democratic-csi PV is no longer needed:

```bash
kubectl delete pv pvc-2cf78549-3392-457e-9119-6a7be7da6707
```

#### Step 7: Apply New Manifests

```bash
kubectl apply -f qbittorrent-volume.yaml
```

Verify the PVC is bound:

```bash
kubectl get pvc config-qbittorrent-0 -n media
# STATUS: Bound
```

#### Step 8: Scale Up Workload

```bash
kubectl scale statefulset qbittorrent -n media --replicas=1

# Verify pod is running and can access data
kubectl get pods -n media -l app=qbittorrent
kubectl logs -n media qbittorrent-0
```

### Protocol-Specific Notes

#### NFS Migration

```bash
kubectl tns-csi import storage/nfs/pvc-xxx --protocol nfs

# If NFS share doesn't exist, create it:
kubectl tns-csi import storage/nfs/pvc-xxx --protocol nfs --create-share
```

#### NVMe-oF Migration

```bash
kubectl tns-csi import storage/nvmeof/v/pvc-xxx --protocol nvmeof
```

Note: NVMe-oF requires the NVMe-oF port to be configured in TrueNAS.

#### iSCSI Migration

```bash
kubectl tns-csi import storage/iscsi/v/pvc-xxx --protocol iscsi
```

Note: iSCSI requires the iSCSI portal to be configured in TrueNAS.

## Migrating from Older tns-csi Versions

Older versions of tns-csi (pre-0.8) used base64-encoded JSON volumeHandles instead of plain volume IDs. These volumes work correctly but won't appear in `kubectl tns-csi list`.

### Identifying Old-Format Volumes

```bash
# Check volumeHandle length (old format is ~316 chars, new is ~40 chars)
kubectl get pv -o json | jq -r '
  .items[] |
  select(.spec.csi.driver == "tns.csi.io") |
  "\(.metadata.name): \(.spec.csi.volumeHandle | length) chars"'
```

### Fixing VolumeHandle Format

The `volumeHandle` field is immutable, so you must recreate the PV/PVC:

1. **Scale down workload**
2. **Set Retain policy on PV**
3. **Delete PVC**
4. **Delete old PV**
5. **Create new PV with plain volumeHandle**
6. **Create new PVC**
7. **Import dataset** (to set ZFS properties so it shows in `tns-csi list`)
8. **Scale up workload**

```bash
# Example: Convert volumeHandle from base64 to plain
# Old PV had: volumeHandle: eyJuYW1lIjoicHZjLTEyMzQ1...
# New PV uses: volumeHandle: pvc-12345-xxxx-xxxx-xxxx

# 1. Get the plain name from the base64
kubectl get pv <pv-name> -o jsonpath='{.spec.csi.volumeHandle}' | base64 -d | jq -r '.name'

# 2. Recreate PV with that plain name as volumeHandle
# 3. Import dataset to set ZFS properties:
kubectl tns-csi import <dataset-path> --protocol nfs
```

## Disaster Recovery

When a Kubernetes cluster is lost but TrueNAS data survives, use this process to recover volumes.

### Step 1: List Available Volumes

Find volumes that were managed by tns-csi:

```bash
kubectl tns-csi list
```

Or find all orphaned volumes (volumes with no matching PVC):

```bash
kubectl tns-csi list-orphaned
```

### Step 2: Generate and Apply Manifests

For each volume to recover:

```bash
kubectl tns-csi adopt <dataset-path> \
  --pvc-name <desired-pvc-name> \
  --namespace <namespace> \
  -o yaml | kubectl apply -f -
```

### Step 3: Redeploy Workloads

Deploy your applications. If using GitOps with the same PVC names, volumes will be automatically bound.

## Automatic Adoption (GitOps)

For GitOps workflows, configure StorageClasses to automatically adopt existing volumes when PVCs with matching names are created.

### StorageClass Configuration

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: truenas-nfs-gitops
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: tank
  parentDataset: csi
  server: truenas.local
  markAdoptable: "true"    # New volumes can be adopted later
  adoptExisting: "true"    # Auto-adopt volumes with matching names
reclaimPolicy: Retain      # Keep volumes on PVC deletion
allowVolumeExpansion: true
```

### How It Works

1. When a PVC is created, the driver searches for an existing volume by name
2. If found and adoptable, the existing volume is used instead of creating new
3. Missing TrueNAS resources (NFS shares, iSCSI targets) are recreated automatically
4. The volume is returned as if newly provisioned, but with existing data

See [FEATURES.md](FEATURES.md#volume-adoption-cross-cluster) for detailed adoption behavior.

## Importing Unmanaged Volumes

To import volumes that were never managed by any CSI driver (manually created):

### Step 1: Discover Unmanaged Volumes

```bash
kubectl tns-csi list-unmanaged --pool storage
```

This shows all datasets/ZVOLs not managed by tns-csi, including:
- Manually created datasets
- Democratic-csi volumes
- Other CSI driver volumes

### Step 2: Import and Adopt

```bash
# Import with NFS protocol (creates share if needed)
kubectl tns-csi import storage/mydata/volume1 --protocol nfs --create-share

# Generate manifests
kubectl tns-csi adopt storage/mydata/volume1 \
  --pvc-name my-volume \
  --namespace default \
  -o yaml > my-volume.yaml

# Apply
kubectl apply -f my-volume.yaml
```

## Troubleshooting

### PVC Stuck in Pending After Adoption

Check that:
1. The PV exists and is in `Available` state
2. The PVC's `volumeName` matches the PV name
3. The StorageClass matches between PV and PVC
4. Access modes match between PV and PVC

```bash
kubectl describe pv <pv-name>
kubectl describe pvc <pvc-name> -n <namespace>
```

### "Volume already managed by tns-csi" Error

The dataset already has tns-csi properties. Either:
- Use `kubectl tns-csi adopt` directly (skip import)
- Or remove existing properties on TrueNAS:
  ```bash
  zfs inherit -r tns-csi:managed_by <dataset>
  ```

### StatefulSet PVC Name Mismatch

StatefulSets expect PVCs with specific names: `<volumeClaimTemplate-name>-<statefulset-name>-<ordinal>`

For a StatefulSet named `postgres` with volumeClaimTemplate `data`:
- Replica 0: `data-postgres-0`
- Replica 1: `data-postgres-1`

Ensure `--pvc-name` matches exactly when adopting.

### NFS Share Missing After Import

If the NFS share was deleted but the dataset exists:

```bash
kubectl tns-csi import <dataset> --protocol nfs --create-share
```

### GitOps Conflicts (Flux/ArgoCD)

If using GitOps, you may see errors like:
```
PVC <name> spec is immutable after creation
```

This happens when your GitOps manifests have a different `storageClassName` than the live PVC.

**Solution:**
1. Suspend the relevant kustomization/application
2. Update your Git manifests to match the new storage class
3. Include both PV and PVC in your manifests (static provisioning)
4. Commit and push
5. Resume reconciliation

```bash
# Flux example
flux suspend kustomization <name>
# ... make changes, commit, push ...
flux resume kustomization <name>
```

### Operators Keep Recreating Pods

If operators (vm-operator, coroot-operator, etc.) keep recreating pods during migration:

```bash
# Scale down the operator first
kubectl scale deploy <operator-name> -n <namespace> --replicas=0

# Do your migration
# ...

# Scale operator back up
kubectl scale deploy <operator-name> -n <namespace> --replicas=1
```

### Data Not Visible in Pod

1. Verify the mount succeeded:
   ```bash
   kubectl exec -it <pod> -- df -h
   kubectl exec -it <pod> -- ls -la /path/to/mount
   ```

2. Check volume attributes match:
   ```bash
   kubectl get pv <pv-name> -o yaml
   ```

3. Verify NFS share path / iSCSI IQN / NVMe NQN is correct

## CLI Command Reference

| Command | Description |
|---------|-------------|
| `kubectl tns-csi list` | List all tns-csi managed volumes |
| `kubectl tns-csi list-orphaned` | Find volumes without matching PVCs |
| `kubectl tns-csi list-unmanaged --pool <pool>` | List volumes not managed by tns-csi |
| `kubectl tns-csi import <dataset> --protocol <proto>` | Import dataset into tns-csi management |
| `kubectl tns-csi adopt <dataset>` | Generate PV/PVC manifests |
| `kubectl tns-csi describe <volume>` | Show detailed volume info |
| `kubectl tns-csi mark-adoptable <volume>` | Mark volume as adoptable |

See [KUBECTL-PLUGIN.md](KUBECTL-PLUGIN.md) for complete CLI documentation.

## Related Documentation

- [FEATURES.md](FEATURES.md) - Full feature documentation including automatic adoption
- [KUBECTL-PLUGIN.md](KUBECTL-PLUGIN.md) - Complete kubectl plugin reference
- [DEPLOYMENT.md](DEPLOYMENT.md) - Installation and configuration guide
