# Quick Start: Testing NVMe-oF and NFS

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development phase. Use only for testing and evaluation environments. Use at your own risk.

This guide explains the testing setup for the TrueNAS CSI driver with both NVMe-oF and NFS protocols.

## Testing Environments

### NVMe-oF Testing → UTM VM

NVMe-oF requires real kernel modules and block device support that isn't available in containers.

**Prerequisites:**
- **TrueNAS Scale 25.10 or later** (NVMe-oF feature introduced in 25.10)

**Why UTM VM?**
- ✅ Full NVMe-oF kernel module support (`nvme-tcp`, `nvme-fabrics`)
- ✅ Real block device operations
- ✅ Network access to TrueNAS
- ✅ Runs Kubernetes (k3s)
- ✅ Native performance on Apple Silicon

**What's tested:**
- Volume provisioning (ZVOL → Namespace)
- NVMe-oF target discovery and connection
- Block device mounting in pods
- I/O operations

### NFS Testing → Kind Cluster

NFS works perfectly in containers and doesn't require special kernel modules.

**Why Kind?**
- ✅ Fast startup (seconds vs minutes)
- ✅ No separate VM needed
- ✅ Perfect for NFS protocol testing
- ✅ Integrated with local Docker

**What's tested:**
- NFS share provisioning
- Volume mounting in pods
- Standard filesystem operations

## NVMe-oF Testing Setup (UTM VM)

### Prerequisites

1. **UTM** installed on macOS - [Download from UTM website](https://mac.getutm.app/)
2. **Ubuntu 22.04 LTS** VM created in UTM with:
   - **CPU:** 4 cores
   - **RAM:** 4 GB
   - **Disk:** 50 GB
   - **Network:** Bridged (to access TrueNAS)
3. **TrueNAS Scale 25.10 or later** server with:
   - NVMe-oF service enabled
   - **⚠️ IMPORTANT: At least one NVMe-oF subsystem with TCP port configured** (see below)
4. **Docker Desktop** for building images

#### ⚠️ Required: Configure NVMe-oF on TrueNAS

**Before provisioning NVMe-oF volumes**, you must complete these configuration steps on TrueNAS 25.10+:

##### Step 1: Configure Static IP Address (REQUIRED)

TrueNAS requires a static IP - DHCP interfaces won't appear in NVMe-oF configuration:

1. **Navigate to:** Network → Interfaces
2. **Edit** your active network interface
3. **Configure:**
   - **DHCP:** Disable
   - **IP Address:** Your static IP (e.g., `YOUR-TRUENAS-IP/24`)
   - **Gateway:** Your network gateway
   - **DNS:** DNS servers (e.g., `8.8.8.8`)
4. **Test Changes** and **Save Changes**

##### Step 2: Create Initial ZVOL (REQUIRED)

Subsystems require at least one namespace with a ZVOL:

1. **Navigate to:** Datasets
2. **Click:** Add Dataset → Create Zvol
3. **Configure:**
   - **Name:** `nvmeof-init`
   - **Size:** `1 GiB`
   - **Block size:** `16K`
4. **Save**

##### Step 3: Create Subsystem with Namespace and Port (REQUIRED)

**⚠️ IMPORTANT:** The CSI driver does NOT create or delete NVMe-oF subsystems. Subsystems are **pre-configured infrastructure** that serve multiple volumes. You must create the subsystem once before deploying the CSI driver.

1. **Navigate to:** Shares → NVMe-oF Subsystems

2. **Click "Add"** to create a new subsystem

3. **Configure subsystem:**
   - **Subsystem Name:** `nqn.2025-01.com.truenas:csi`
   - **Namespace:** Select the ZVOL you created (e.g., `pool1/nvmeof-init`)

4. **Save** the subsystem

5. **Click "Add Port"** on your new subsystem

6. **Configure port:**
   - **Address:** Select your interface with static IP (should now appear in dropdown)
   - **Port:** `4420` (default NVMe-oF TCP port)
   - **Transport:** `TCP`

7. **Save** the port configuration

8. **Verify:** The subsystem shows:
   - At least one namespace (your ZVOL)
   - At least one TCP port

9. **Note the subsystem NQN** - you'll need this for your StorageClass configuration (e.g., `nqn.2025-01.com.truenas:csi`)

**Why is this required?**

- **Static IP:** TrueNAS only allows NVMe-oF on interfaces with static IPs (prevents storage outages from IP changes)
- **Initial Namespace:** Empty subsystems are invalid - must have at least one ZVOL namespace
- **Port:** CSI driver cannot create ports - must be pre-configured
- **Shared Infrastructure:** One subsystem serves multiple volumes (namespaces). The CSI driver creates/deletes only namespaces, not subsystems.

**Architecture:**
- **Subsystem:** Pre-configured infrastructure (created by administrator, never deleted by CSI driver)
- **Namespaces:** Dynamically created/deleted by CSI driver for each PVC
- **1 Subsystem → Many Namespaces (Volumes)**

**What happens if not configured?**

Volume provisioning will fail with:
```
Failed to find NVMe-oF subsystem with NQN '<your-nqn>'.
Pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems)
with ports attached before provisioning volumes.
```

### VM Setup

1. **Create Ubuntu VM in UTM:**
   - Download Ubuntu 22.04 Server ISO
   - Create new VM with Virtualization mode
   - Configure bridged networking

2. **Install required packages in VM:**
   ```bash
   # SSH into your UTM VM
   ssh <user>@<vm-ip>
   
   # Install NVMe tools
   sudo apt-get update
   sudo apt-get install -y nvme-cli curl
   
   # Load NVMe-oF kernel modules
   sudo modprobe nvme-tcp
   sudo modprobe nvme-fabrics
   
   # Make modules load on boot
   echo "nvme-tcp" | sudo tee -a /etc/modules
   echo "nvme-fabrics" | sudo tee -a /etc/modules
   ```

3. **Install k3s:**
   ```bash
   curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644
   
   # Wait for k3s to be ready
   sudo kubectl get nodes
   ```

4. **Configure kubectl from macOS:**
   ```bash
   # Copy kubeconfig from VM
   ssh <user>@<vm-ip> sudo cat /etc/rancher/k3s/k3s.yaml > ~/.kube/utm-nvmeof-test
   
   # Update server address
   VM_IP=<your-vm-ip>
   sed -i.bak "s|127.0.0.1|${VM_IP}|g" ~/.kube/utm-nvmeof-test
   
   # Test connection
   kubectl --kubeconfig ~/.kube/utm-nvmeof-test get nodes
   ```

### Deploy CSI Driver to UTM VM

```bash
# Build the CSI driver
make build-image

# Save and transfer to VM
docker save tns-csi-driver:latest | gzip > tns-csi-driver.tar.gz
scp tns-csi-driver.tar.gz <user>@<vm-ip>:~

# Load into k3s on VM
ssh <user>@<vm-ip> 'sudo k3s ctr images import tns-csi-driver.tar.gz'

# Deploy with Helm
export KUBECONFIG=~/.kube/utm-nvmeof-test
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --set truenas.host=YOUR-TRUENAS-IP \
  --set truenas.apiKey=<your-api-key> \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool=<your-pool-name> \
  --set storageClasses.nvmeof.server=YOUR-TRUENAS-IP \
  --set storageClasses.nvmeof.subsystemNQN=nqn.2025-01.com.truenas:csi
```

**Important:** Replace `nqn.2025-01.com.truenas:csi` with the actual subsystem NQN you created in Step 3 (line 99).

### Test NVMe-oF Volume

```bash
export KUBECONFIG=~/.kube/utm-nvmeof-test

# Create PVC
kubectl apply -f deploy/example-nvmeof-pvc.yaml

# Create pod
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-nvmeof-pod
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
      claimName: test-nvmeof-pvc
EOF

# Verify pod is running
kubectl get pod test-nvmeof-pod

# Check NVMe devices
ssh <user>@<vm-ip> 'sudo nvme list'

# Test I/O
kubectl exec test-nvmeof-pod -- dd if=/dev/zero of=/data/test bs=1M count=100
```

## NFS Testing Setup (Kind Cluster)

NFS testing is much simpler since it works in containers:

### Prerequisites

1. **Kind** installed: `brew install kind`
2. **Docker Desktop** running
3. **TrueNAS Scale** server accessible

### Setup and Test

```bash
# Create Kind cluster
kind create cluster --name tns-csi-test

# Build and load image
make build-image
kind load docker-image tns-csi-driver:latest --name tns-csi-test

# Deploy CSI driver
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --set truenas.host=YOUR-TRUENAS-IP \
  --set truenas.apiKey=<your-api-key>

# Test NFS volume
kubectl apply -f deploy/example-pvc.yaml
kubectl apply -f deploy/test-pod.yaml

# Verify
kubectl get pvc
kubectl get pod test-nfs-pod
```

## Daily Workflow

### Working on NVMe-oF features:

```bash
# 1. Edit code on macOS
vim pkg/driver/node.go

# 2. Build and deploy to UTM VM
make build-image
docker save tns-csi-driver:latest | gzip > tns-csi-driver.tar.gz
scp tns-csi-driver.tar.gz <user>@<vm-ip>:~
ssh <user>@<vm-ip> 'sudo k3s ctr images import tns-csi-driver.tar.gz'

# 3. Restart CSI driver pods
export KUBECONFIG=~/.kube/utm-nvmeof-test
kubectl rollout restart -n kube-system daemonset/tns-csi-node

# 4. View logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin -f
```

### Working on NFS features:

```bash
# 1. Edit code on macOS
vim pkg/driver/controller.go

# 2. Build and load to Kind
make build-image
kind load docker-image tns-csi-driver:latest --name tns-csi-test

# 3. Restart pods
kubectl rollout restart -n kube-system deployment/tns-csi-controller

# 4. Test
kubectl apply -f deploy/example-pvc.yaml
```

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                       macOS Host                            │
│  - Code editing                                             │
│  - Docker builds                                            │
│  - kubectl access to both clusters                          │
└──────────────┬───────────────────────────┬──────────────────┘
               │                           │
     ┌─────────▼────────┐        ┌────────▼─────────┐
     │   UTM VM         │        │  Kind Cluster    │
     │   (Ubuntu)       │        │  (Containers)    │
     │                  │        │                  │
     │  - k3s           │        │  - Kubernetes    │
     │  - NVMe modules  │        │  - NFS only      │
     │  - CSI driver    │        │  - CSI driver    │
     └────────┬─────────┘        └────────┬─────────┘
              │                           │
              └────────────┬──────────────┘
                           │
                  ┌────────▼─────────┐
                  │   TrueNAS Scale  │
                  │  - NVMe-oF Target│
                  │  - NFS Server    │
                  │  - ZFS Pools     │
                  └──────────────────┘
```

## Summary

| Protocol | Environment | Setup Time | Best For |
|----------|-------------|------------|----------|
| **NVMe-oF** | UTM VM | 15 min (one-time) | Block storage, performance testing |
| **NFS** | Kind Cluster | 2 min | Fast iteration, file storage |

## Next Steps

1. **For NVMe-oF development:** Set up UTM VM following steps above
2. **For NFS development:** Use existing Kind cluster setup
3. **Read full docs:** See `NVMEOF_TESTING.md` for detailed UTM setup
4. **Add tests:** Create test scenarios for your use cases

## Troubleshooting

### UTM VM Issues
- Ensure bridged networking is configured
- Verify VM can reach TrueNAS: `ping YOUR-TRUENAS-IP`
- Check NVMe modules: `lsmod | grep nvme`

### Kind Cluster Issues
- Restart Docker Desktop if cluster won't start
- Reload image if changes aren't reflected
- Check logs: `kubectl logs -n kube-system <pod-name>`

---

**Ready to test?**
- **NVMe-oF:** Set up UTM VM and test block storage
- **NFS:** Use Kind cluster for quick testing
