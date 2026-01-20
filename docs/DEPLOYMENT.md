# TrueNAS Scale CSI Driver - Deployment Guide

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

This guide explains how to deploy the TrueNAS Scale CSI driver on a Kubernetes cluster.

## Prerequisites

1. **Kubernetes Cluster**: Version 1.27 or later (earlier versions may work but are not tested)
2. **TrueNAS Scale**: Version 25.10 or later with API access (NVMe-oF support requires 25.10+)
3. **Network Access**: Kubernetes nodes must be able to reach TrueNAS server
4. **Storage Protocol Requirements**:
   
   **For NFS Support:**
   ```bash
   # Install on Ubuntu/Debian
   sudo apt-get install -y nfs-common
   
   # Install on RHEL/CentOS
   sudo yum install -y nfs-utils
   ```
   
   **For NVMe-oF Support:**
   ```bash
   # Install nvme-cli tools on all nodes
   # Ubuntu/Debian
   sudo apt-get install -y nvme-cli

   # RHEL/CentOS
   sudo yum install -y nvme-cli

   # Load NVMe-oF kernel modules
   sudo modprobe nvme-tcp

   # Make module loading persistent
   echo "nvme-tcp" | sudo tee /etc/modules-load.d/nvme.conf

   # Verify nvme-cli is installed
   nvme version
   ```

   **For iSCSI Support:**
   ```bash
   # Install open-iscsi on all nodes
   # Ubuntu/Debian
   sudo apt-get install -y open-iscsi

   # RHEL/CentOS
   sudo yum install -y iscsi-initiator-utils

   # Enable and start iscsid service
   sudo systemctl enable iscsid
   sudo systemctl start iscsid

   # Verify iSCSI is installed
   iscsiadm --version
   ```

## Step 1: Prepare TrueNAS Scale

### 1.1 Create API Key

1. Log in to TrueNAS Scale web interface
2. Navigate to **System Settings** > **API Keys**
3. Click **Add**
4. Give it a name (e.g., "kubernetes-csi")
5. Copy the generated API key (you won't be able to see it again)

### 1.2 Create Storage Pool

If you don't already have a pool:
1. Navigate to **Storage** > **Create Pool**
2. Follow the wizard to create a pool (e.g., "pool1")

### 1.3 (Optional) Create Parent Dataset

For better organization, create a parent dataset for Kubernetes volumes:
1. Navigate to **Datasets**
2. Select your pool
3. Click **Add Dataset**
4. Name it (e.g., "k8s")
5. Keep default settings and click **Save**

### 1.4 Enable NVMe-oF Service (For NVMe-oF Support)

**⚠️ IMPORTANT:** NVMe-oF requires pre-configuration before volume provisioning will work.

If you plan to use NVMe-oF storage:

#### Enable the NVMe-oF Service

1. Navigate to **System Settings** > **Services**
2. Find **NVMe-oF** service
3. Click the toggle to enable it
4. Click **Save** and verify the service is running

#### Configure Static IP Address (REQUIRED)

**TrueNAS requires a static IP address for NVMe-oF** - you cannot use DHCP:

1. Navigate to **Network** → **Interfaces**
2. Find your active network interface (e.g., `enp0s1`, `eth0`)
3. Click **Edit**
4. Configure static IP:
   - **DHCP:** Uncheck/disable
   - **IP Address:** Enter your static IP (e.g., `YOUR-TRUENAS-IP/24`)
   - **Gateway:** Enter your network gateway
   - **DNS Servers:** Add DNS servers (e.g., `8.8.8.8`)
5. Click **Save** and **Test Changes**
6. After testing, click **Save Changes** to make it permanent

**Why is this required?**

TrueNAS 25.10 only shows interfaces with static IPs in the NVMe-oF port configuration. DHCP addresses can change on reboot, which would break storage connections.

#### Create Initial ZVOL and Namespace (REQUIRED)

**The subsystem needs at least one namespace with a ZVOL** - empty subsystems won't work:

1. Navigate to **Datasets**
2. Click **Add Dataset** → **Create Zvol**
3. Configure the ZVOL:
   - **Name:** `nvmeof-init` (or any name)
   - **Size:** `1 GiB` (minimum size for initial namespace)
   - **Block size:** `16K` (recommended)
4. Click **Save**

This creates the ZVOL needed for the initial namespace.

#### Configure NVMe-oF Subsystem with Port and Namespace (REQUIRED)

**⚠️ ARCHITECTURE NOTE:** The CSI driver uses a **shared subsystem model**:
- **1 Subsystem → Many Namespaces** (one namespace per PVC)
- The subsystem is **pre-configured infrastructure** (like a storage pool)
- The CSI driver creates **namespaces** (not subsystems) for each volume

**Now create the subsystem with namespace and port:**

1. Navigate to **Shares** → **NVMe-oF Subsystems**
2. Click **Add** to create a new subsystem
3. Configure the subsystem:
   - **Subsystem Name:** Enter a qualified name (e.g., `nqn.2005-03.org.truenas:csi`)
     - **IMPORTANT:** Remember this NQN - you'll need it for the StorageClass configuration
   - **Namespace:** Select the ZVOL you just created (e.g., `pool1/nvmeof-init`)
4. Click **Save**
5. After creating the subsystem, click **Add Port**
6. Configure the port:
   - **Address:** Select your network interface with static IP (should now appear in dropdown)
   - **Port:** `4420` (default NVMe-oF TCP port)
   - **Transport:** `TCP`
7. Click **Save**
8. Verify the subsystem appears in the list with:
   - At least one namespace (the ZVOL you created)
   - At least one TCP port configured

**Why is this required?**

- **Static IP:** TrueNAS only allows binding NVMe-oF to interfaces with static IPs
- **Initial Namespace/ZVOL:** Empty subsystems are not valid - you need at least one namespace
- **Port:** The CSI driver cannot create ports - they must be pre-configured
- **Shared Subsystem:** All CSI-provisioned volumes share this subsystem as separate namespaces

The CSI driver will create additional namespaces automatically for each PVC within this shared subsystem.

Without proper configuration, volume provisioning will fail with:

```
No TCP NVMe-oF port configured on TrueNAS server. 
Please configure an NVMe-oF TCP port in TrueNAS before provisioning NVMe-oF volumes.
```

Or if the `subsystemNQN` parameter is missing:

```
Parameter 'subsystemNQN' is required for nvmeof protocol
```

### 1.5 Enable iSCSI Service (For iSCSI Support)

**⚠️ IMPORTANT:** iSCSI requires pre-configuration before volume provisioning will work.

If you plan to use iSCSI storage:

#### Enable the iSCSI Service

1. Navigate to **System Settings** > **Services**
2. Find **iSCSI** service
3. Click the toggle to enable it
4. Click **Save** and verify the service is running

#### Configure a Portal (REQUIRED)

A portal defines where iSCSI targets listen for connections:

1. Navigate to **Shares** → **Block (iSCSI)** → **Portals**
2. Click **Add**
3. Configure the portal:
   - **Description:** `kubernetes-csi` (or any descriptive name)
   - **IP Address:** Select your TrueNAS IP address (or `0.0.0.0` to listen on all interfaces)
   - **Port:** `3260` (default iSCSI port)
4. Click **Save**

#### Configure an Initiator Group (REQUIRED)

An initiator group controls which hosts can connect to iSCSI targets:

1. Navigate to **Shares** → **Block (iSCSI)** → **Initiators**
2. Click **Add**
3. Configure the initiator group:
   - **Description:** `kubernetes-csi` (or any descriptive name)
   - **Connected Initiators:** Leave empty to allow all initiators, or add specific IQNs for security
   - **Authorized Networks:** Leave empty to allow all networks, or specify CIDR ranges (e.g., `10.0.0.0/8`)
4. Click **Save**

**Why is this required?**

- **Portal:** Defines the network endpoint where iSCSI targets listen - required for any iSCSI connectivity
- **Initiator Group:** Controls access to targets - the CSI driver uses this when creating targets

The CSI driver will automatically create targets, extents, and target-extent associations for each PVC.

Without proper configuration, volume provisioning will fail with:

```
No iSCSI portal configured on TrueNAS server.
Please configure an iSCSI portal in TrueNAS before provisioning iSCSI volumes.
```

Or:

```
No iSCSI initiator group configured on TrueNAS server.
Please configure an iSCSI initiator group in TrueNAS before provisioning iSCSI volumes.
```

#### Architecture Comparison: iSCSI vs NVMe-oF

| Aspect | iSCSI | NVMe-oF |
|--------|-------|---------|
| Pre-configuration | Portal + Initiator group | Subsystem + Port + Initial namespace |
| Per-volume creation | Target + Extent + Association | Namespace in shared subsystem |
| Target model | Dedicated target per volume | Shared subsystem, namespace per volume |
| Complexity | Simpler setup | More complex initial setup |

## Step 2: Install Using Helm (Recommended)

The easiest way to deploy the CSI driver is using the Helm chart from Docker Hub:

**For NFS:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

**For NVMe-oF:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.0 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2005-03.org.truenas:csi"
```

**Note:** Replace `nqn.2005-03.org.truenas:csi` with the actual subsystem NQN you configured in Step 1.4 (line 99).

**For iSCSI:**
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

**Note:** iSCSI requires a portal and initiator group to be pre-configured. See Step 1.5 for setup instructions.

This single command will:
- Create the kube-system namespace if needed
- Deploy the CSI controller and node components
- Configure TrueNAS connection
- Create the storage class

See the [Helm chart README](charts/tns-csi-driver/README.md) for advanced configuration options.

**Skip to Step 5 (Verify Installation) if using Helm installation.**

---

<details>
<summary>Alternative: Manual Deployment with kubectl - Click to expand</summary>

For advanced users who prefer manual deployment without Helm:

### Step 2a: Build and Push Docker Image (Optional)

If you want to build your own image instead of using the published one:

```bash
# From the project root directory
make build

# Build Docker image
docker build -t your-registry/tns-csi-driver:v0.0.1 .

# Push to your registry (DockerHub, GitHub Container Registry, etc.)
docker push your-registry/tns-csi-driver:v0.0.1
```

If using a private registry, ensure your Kubernetes cluster has pull access.

The published image is available at: `bfenski/tns-csi:v0.0.1`

## Step 3: Configure Deployment Manifests (Manual Deployment Only)

### 3.1 Update Secret

Edit `deploy/secret.yaml` and replace placeholders:

```yaml
stringData:
  # WebSocket URL (use ws:// for HTTP or wss:// for HTTPS)
  url: "ws://YOUR-TRUENAS-IP/websocket"
  # API key from Step 1.1
  api-key: "1-abcdef123456789..."
```

### 3.2 Update Image References

Edit `deploy/controller.yaml` and `deploy/node.yaml`:

Replace:
```yaml
image: your-registry/tns-csi-driver:latest
```

With:
```yaml
image: your-registry/tns-csi-driver:v0.0.1
```

### 3.3 Update StorageClass

Edit `deploy/storageclass.yaml` and configure parameters:

**For NFS:**
```yaml
parameters:
  protocol: "nfs"
  pool: "pool1"              # Your TrueNAS pool name
  # parentDataset: "pool1/k8s"  # Optional parent dataset
  server: "YOUR-TRUENAS-IP"     # Your TrueNAS IP/hostname
  # Optional parameters:
  # deleteStrategy: "retain"     # Keep volumes on TrueNAS when PVC deleted
  # zfs.compression: "lz4"       # ZFS compression algorithm
  # zfs.recordsize: "128K"       # ZFS record size
```

**For NVMe-oF:**
```yaml
parameters:
  protocol: "nvmeof"
  pool: "storage"                                          # Your TrueNAS pool name
  server: "YOUR-TRUENAS-IP"                                # Your TrueNAS IP/hostname
  subsystemNQN: "nqn.2005-03.org.truenas:csi"              # REQUIRED: The subsystem NQN from Step 1.4
  # Optional parameters:
  # filesystem: "ext4"                                     # Filesystem type: ext4 (default), ext3, or xfs
  # blocksize: "16K"                                       # Block size for ZVOL (default: 16K)
  # deleteStrategy: "retain"                               # Keep volumes on TrueNAS when PVC deleted
  # zfs.compression: "lz4"                                 # ZFS compression algorithm
```

**For iSCSI:**
```yaml
parameters:
  protocol: "iscsi"
  pool: "storage"                                          # Your TrueNAS pool name
  server: "YOUR-TRUENAS-IP"                                # Your TrueNAS IP/hostname
  # Optional parameters:
  # port: "3260"                                           # iSCSI port (default: 3260)
  # filesystem: "ext4"                                     # Filesystem type: ext4 (default), ext3, or xfs
  # blocksize: "16K"                                       # Block size for ZVOL (default: 16K)
  # deleteStrategy: "retain"                               # Keep volumes on TrueNAS when PVC deleted
  # zfs.compression: "lz4"                                 # ZFS compression algorithm
```

**Important Notes:**
- `subsystemNQN` is **REQUIRED** for NVMe-oF - it must match the subsystem you created in Step 1.4
- The CSI driver creates **namespaces** within this shared subsystem (not new subsystems per volume)
- NVMe-oF and iSCSI volumes use `ReadWriteOnce` access mode (block storage), while NFS uses `ReadWriteMany` (shared filesystem)
- iSCSI creates dedicated targets per volume automatically

## Step 4: Deploy to Kubernetes (Manual Deployment Only)

### 4.1 Deploy CSI Driver

Apply manifests in the following order:

```bash
# 1. Create secret with TrueNAS credentials
kubectl apply -f deploy/secret.yaml

# 2. Create RBAC resources
kubectl apply -f deploy/rbac.yaml

# 3. Create CSIDriver resource
kubectl apply -f deploy/csidriver.yaml

# 4. Deploy controller (StatefulSet)
kubectl apply -f deploy/controller.yaml

# 5. Deploy node plugin (DaemonSet)
kubectl apply -f deploy/node.yaml

# 6. Create StorageClass
kubectl apply -f deploy/storageclass.yaml
```

### 4.2 Verify Deployment

```bash
# Check controller pod
kubectl get pods -n kube-system -l app=tns-csi-controller

# Check node pods (should be one per node)
kubectl get pods -n kube-system -l app=tns-csi-node

# Check CSIDriver
kubectl get csidrivers

# Check StorageClass
kubectl get storageclass
```

Expected output:
```
NAME                              READY   STATUS    RESTARTS   AGE
tns-csi-controller-0          5/5     Running   0          1m
tns-csi-node-xxxxx            2/2     Running   0          1m
tns-csi-node-yyyyy            2/2     Running   0          1m
```

</details>

---

## Step 5: Verify Installation

Whether you used Helm or manual deployment, verify everything is working:

```bash
# Check controller pod
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# Check CSIDriver
kubectl get csidrivers

# Check StorageClass
kubectl get storageclass
```

For Helm installations, the storage class name will be `truenas-nfs` (or as configured).
For manual installations, it will be as defined in your `storageclass.yaml`.

## Step 6: Test the Driver

### 5.1 Create Test PVC

**For NFS:**
```bash
kubectl apply -f deploy/example-pvc.yaml
```

**For NVMe-oF:**
```bash
kubectl apply -f deploy/example-nvmeof-pvc.yaml
```

### 5.2 Verify PVC is Bound

```bash
kubectl get pvc test-pvc

# Expected output:
# NAME       STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS    AGE
# test-pvc   Bound    pvc-12345678-1234-1234-1234-123456789012   10Gi       RWX            tns-nfs     30s
```

### 5.3 Verify in TrueNAS

**For NFS volumes:**
1. Log in to TrueNAS web interface
2. Navigate to **Datasets**
3. You should see a new dataset: `pool1/test-pvc` (or `pool1/k8s/test-pvc` if using parent dataset)
4. Navigate to **Shares** > **NFS**
5. You should see a new NFS share for the dataset

**For NVMe-oF volumes:**
1. Log in to TrueNAS web interface
2. Navigate to **Datasets**
3. You should see a new ZVOL (block device): `pool1/test-nvmeof-pvc`
4. Navigate to **Shares** > **NVMe-oF Subsystems**
5. Click on your subsystem (e.g., `nqn.2005-03.org.truenas:csi`)
6. You should see a **new namespace** added to the subsystem for the PVC
   - The subsystem itself remains the same (shared infrastructure)
   - Each PVC gets its own namespace within the subsystem
7. On the Kubernetes node, verify the NVMe device is connected:
   ```bash
   # List NVMe devices
   sudo nvme list
   
   # Check specific connection
   kubectl exec test-nvmeof-pod -- df -h /data
   ```

### 5.4 Create Test Pod

The example manifest includes a test pod. Verify it's running:

```bash
kubectl get pod test-pod

# Check if volume is mounted
kubectl exec test-pod -- df -h /data
```

### 5.5 Cleanup Test Resources

```bash
kubectl delete -f deploy/example-pvc.yaml
```

Verify the dataset and NFS share are removed from TrueNAS (if reclaimPolicy is Delete).

## Troubleshooting

### Check Controller Logs

For Helm deployments:
```bash
# Get controller pod logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -c tns-csi-plugin
```

For manual (kubectl) deployments:
```bash
kubectl logs -n kube-system tns-csi-controller-0 -c tns-csi-plugin
```

### Check Node Plugin Logs

For Helm deployments:
```bash
# Get node plugin pod name
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node

# View logs (replace xxxxx with actual pod name)
kubectl logs -n kube-system tns-csi-node-xxxxx -c tns-csi-plugin
```

For manual (kubectl) deployments:
```bash
# Get node plugin pod name
kubectl get pods -n kube-system -l app=tns-csi-node

# View logs
kubectl logs -n kube-system tns-csi-node-xxxxx -c tns-csi-plugin
```

### Common Issues

1. **Pod stuck in ContainerCreating**
   - Check node plugin logs
   - Verify NFS client is installed on nodes (for NFS)
   - Verify nvme-cli is installed on nodes (for NVMe-oF)
   - Check network connectivity to TrueNAS

2. **PVC stuck in Pending**
   - Check controller logs
   - Verify TrueNAS credentials in secret
   - Check TrueNAS pool has available space

3. **Authentication failures**
   - Verify API key is correct
   - Check TrueNAS API is accessible: `curl http://YOUR-TRUENAS-IP/api/docs/`

4. **NFS mount failures**
   - Verify NFS service is enabled on TrueNAS
   - Check firewall rules allow NFS traffic (port 2049)
   - Verify NFS share exists in TrueNAS

5. **NVMe-oF connection failures**
   - Verify nvme-cli is installed: `nvme version`
   - Check NVMe-oF kernel module is loaded: `lsmod | grep nvme_tcp`
   - Verify NVMe-oF service is running on TrueNAS
   - Check firewall allows port 4420 (default NVMe-oF TCP port)
   - Test connectivity: `sudo nvme discover -t tcp -a YOUR-TRUENAS-IP -s 4420`
   - Check node plugin logs for detailed error messages

6. **NVMe device not appearing**
   - Wait a few seconds for device discovery
   - Check dmesg for NVMe errors: `sudo dmesg | grep nvme`
   - Verify subsystem exists: `sudo nvme list-subsys`
   - Check /sys/class/nvme for device entries

7. **iSCSI connection failures**
   - Verify open-iscsi is installed: `iscsiadm --version`
   - Check iscsid service is running: `systemctl status iscsid`
   - Verify iSCSI service is enabled on TrueNAS
   - Check firewall allows port 3260 (default iSCSI port)
   - Test discovery: `sudo iscsiadm -m discovery -t sendtargets -p YOUR-TRUENAS-IP:3260`
   - Check node plugin logs for detailed error messages

8. **iSCSI device not appearing**
   - Wait a few seconds for device discovery
   - Check dmesg for SCSI errors: `sudo dmesg | grep -i scsi`
   - List active sessions: `sudo iscsiadm -m session`
   - Check /dev/disk/by-path for iSCSI entries

### kubectl Plugin for Troubleshooting

The `kubectl tns-csi` plugin provides powerful troubleshooting capabilities:

```bash
# Install via krew (recommended)
kubectl krew install tns-csi

# Or download from GitHub releases
```

**Troubleshooting Commands:**
```bash
# Comprehensive PVC diagnostics
kubectl tns-csi troubleshoot <pvc-name> -n <namespace> --logs

# Check health of all volumes
kubectl tns-csi health

# Test TrueNAS connectivity
kubectl tns-csi connectivity

# Show detailed volume information
kubectl tns-csi describe <volume-id>
```

**Management Commands:**
```bash
# Dashboard overview
kubectl tns-csi summary

# List all managed volumes
kubectl tns-csi list

# Find orphaned volumes (exist on TrueNAS but no PVC)
kubectl tns-csi list-orphaned

# Clean up orphaned volumes
kubectl tns-csi cleanup --execute
```

See [kubectl Plugin Documentation](KUBECTL-PLUGIN.md) for full command reference.

### Enable Debug Logging

Edit the deployment and increase verbosity:

```yaml
args:
  - "--v=5"  # Change to --v=10 for more verbose output
```

Then restart the pods:

For Helm deployments:
```bash
# Restart controller
kubectl rollout restart statefulset -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller

# Restart node plugin
kubectl rollout restart daemonset -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node
```

For manual (kubectl) deployments:
```bash
kubectl rollout restart statefulset -n kube-system tns-csi-controller
kubectl rollout restart daemonset -n kube-system tns-csi-node
```

## Uninstall

### Helm Installation

To uninstall a Helm deployment:

```bash
# Delete test resources first (if any)
kubectl delete pvc test-pvc

# Uninstall the Helm release
helm uninstall tns-csi -n kube-system
```

### Manual Installation

To remove a manual kubectl deployment:

```bash
# Delete test resources
kubectl delete -f deploy/example-pvc.yaml

# Delete StorageClass
kubectl delete -f deploy/storageclass.yaml

# Delete driver components
kubectl delete -f deploy/node.yaml
kubectl delete -f deploy/controller.yaml
kubectl delete -f deploy/csidriver.yaml
kubectl delete -f deploy/rbac.yaml
kubectl delete -f deploy/secret.yaml
```

## Upgrading

### Standard Upgrade (Minor Versions)

For minor version upgrades within the same schema version:

```bash
# Helm upgrade
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version <NEW_VERSION> \
  --namespace kube-system \
  --reuse-values
```

### Breaking Change Upgrade (v0.6.x → v0.8.0+)

**⚠️ IMPORTANT:** Version 0.8.0 introduces Schema v1 which is a breaking change.

Volumes created with earlier versions will **not be recognized** by the new driver because:
- The metadata schema has changed
- Legacy snapshot ID formats are no longer supported
- Volume lookup fallbacks have been removed

#### Option 1: Fresh Start (Recommended for Dev/Test)

1. Delete all PVCs using the driver:
   ```bash
   kubectl delete pvc -l app.kubernetes.io/provisioner=tns.csi.io --all-namespaces
   ```

2. Upgrade the driver:
   ```bash
   helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
     --version 0.9.0 \
     --namespace kube-system \
     --reuse-values
   ```

3. Re-create your PVCs (data will be new)

#### Option 2: Retain Data with Adoption Workflow

1. **Before upgrading**, update StorageClasses to retain volumes:
   ```yaml
   parameters:
     deleteStrategy: "retain"
   ```

2. Delete PVCs (underlying data is retained on TrueNAS):
   ```bash
   kubectl delete pvc my-important-data
   ```

3. Upgrade the driver to v0.8.0+

4. **Verify volumes have metadata** on TrueNAS:
   ```bash
   # SSH to TrueNAS and check properties
   # Path format: {pool}/{parentDataset}/{volume} or {pool}/{volume}
   # Example with parentDataset=csi:
   zfs get all tank/csi/my-volume | grep tns-csi
   # Example without parentDataset:
   zfs get all tank/my-volume | grep tns-csi
   ```

5. **If metadata exists**, follow the [Volume Adoption workflow](FEATURES.md#volume-adoption-cross-cluster) to re-import volumes

6. **If metadata is missing**, you'll need to manually set properties:
   ```bash
   # On TrueNAS (example for NFS volume at tank/my-volume)
   zfs set tns-csi:managed_by=tns-csi tank/my-volume
   zfs set tns-csi:schema_version=1 tank/my-volume
   zfs set tns-csi:csi_volume_name=my-volume tank/my-volume
   zfs set tns-csi:protocol=nfs tank/my-volume
   zfs set tns-csi:nfs_share_path=/mnt/tank/my-volume tank/my-volume
   # ... set other properties as needed
   ```

#### Verifying Upgrade Success

After upgrading, verify the new driver is working:

```bash
# Check driver version
kubectl logs -n kube-system deployment/tns-csi-controller 2>&1 | head -1

# Test creating a new volume
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: upgrade-test
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: truenas-nfs
EOF

# Verify volume was created with new schema
kubectl get pvc upgrade-test
# On TrueNAS (path depends on your pool/parentDataset settings):
# zfs get tns-csi:schema_version tank/upgrade-test
```

## Production Considerations

1. **High Availability**: Increase controller replicas for HA
   ```yaml
   spec:
     replicas: 3  # In controller.yaml
   ```

2. **Resource Limits**: Adjust CPU/memory limits based on workload

3. **Security**:
   - Use HTTPS/WSS for TrueNAS API connection
   - Implement network policies
   - Use encrypted storage classes
   - Regularly rotate API keys

4. **Monitoring**: Set up monitoring for CSI driver metrics

5. **Backup**: Ensure TrueNAS pool has proper backup strategy

## Protocol Support

This CSI driver supports multiple storage protocols:

- **NFS** (Network File System): Shared filesystem storage with `ReadWriteMany` support
- **NVMe-oF** (NVMe over Fabrics): High-performance block storage with `ReadWriteOnce` support
- **iSCSI** (Internet SCSI): Traditional block storage with broad compatibility and `ReadWriteOnce` support

## Current Capabilities

The following features are fully implemented and tested:

- **Volume Provisioning**: Dynamic creation and deletion of NFS, NVMe-oF, and iSCSI volumes
- **Volume Expansion**: Resize volumes dynamically (`allowVolumeExpansion: true` in StorageClass)
- **Volume Retention**: Optional `deleteStrategy: retain` to keep volumes on PVC deletion
- **Configurable Mount Options**: Custom mount options via StorageClass `mountOptions` field
- **Configurable ZFS Properties**: Set compression, dedup, recordsize, etc. via StorageClass parameters
- **Snapshots**: CSI snapshot support using TrueNAS snapshots (see [SNAPSHOTS.md](SNAPSHOTS.md))
- **Volume Cloning**: Create new volumes from snapshots
- **Volume Health Monitoring**: CSI `GET_VOLUME` capability for Kubernetes volume health reporting
- **Metrics**: Prometheus metrics endpoint (see [METRICS.md](METRICS.md))

## Future Enhancements

Potential future enhancements:

- **Topology**: Add topology awareness for multi-zone deployments

Note: SMB/CIFS is not supported due to the Linux-focused nature of this driver.
