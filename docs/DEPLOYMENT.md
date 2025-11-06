# TrueNAS Scale CSI Driver - Deployment Guide

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

This guide explains how to deploy the TrueNAS Scale CSI driver on a Kubernetes cluster.

## Prerequisites

1. **Kubernetes Cluster**: Version 1.20 or later
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
   - **IP Address:** Enter your static IP (e.g., `10.10.20.100/24`)
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

## Step 2: Install Using Helm (Recommended)

The easiest way to deploy the CSI driver is using the Helm chart from Docker Hub:

**For NFS:**
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

**For NVMe-oF:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
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
```

**Important Notes:**
- `subsystemNQN` is **REQUIRED** for NVMe-oF - it must match the subsystem you created in Step 1.4
- The CSI driver creates **namespaces** within this shared subsystem (not new subsystems per volume)
- NVMe-oF volumes use `ReadWriteOnce` access mode (block storage), while NFS uses `ReadWriteMany` (shared filesystem)

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

## Next Steps

Future enhancements planned:

- **Snapshots**: Implement CSI snapshot support using TrueNAS snapshots
- **Volume Cloning**: Implement CSI volume cloning using TrueNAS clone features
- **Metrics**: Add Prometheus metrics endpoint
- **Topology**: Add topology awareness for multi-zone deployments
- **Additional Protocols**: iSCSI and SMB support may be considered based on community demand

Note: Volume expansion is already supported via Kubernetes when `allowVolumeExpansion: true` is set in the StorageClass (enabled by default in the Helm chart for NFS).
