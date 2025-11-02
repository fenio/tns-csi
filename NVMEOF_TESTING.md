# Testing Guide: NVMe-oF and NFS

This guide explains how to test both NVMe-oF and NFS functionality of the TrueNAS CSI driver on macOS.

## Testing Strategy

The CSI driver supports two protocols, each requiring different test environments:

### NVMe-oF Testing → UTM Virtual Machine

**Why separate VM?**
- macOS (especially Apple Silicon) lacks NVMe-oF initiator support
- Requires real kernel modules (`nvme-tcp`, `nvme-fabrics`) 
- Needs actual block device operations
- Container environments can't provide this

**Solution:** UTM VM with Ubuntu + k3s + NVMe-oF tools

### NFS Testing → Kind Cluster

**Why containers work:**
- NFS works perfectly in container environments
- No special kernel modules needed
- Fast iteration and testing

**Solution:** Kind (Kubernetes in Docker) cluster

## UTM VM Setup for NVMe-oF Testing

### Prerequisites

1. **macOS** (Intel or Apple Silicon)
2. **UTM** - [Download from UTM website](https://mac.getutm.app/)
3. **Ubuntu 22.04 Server ISO** - [Download](https://ubuntu.com/download/server)
4. **Docker Desktop** - For building CSI driver images
5. **TrueNAS Scale** server on your network with:
   - API key generated
   - NVMe-oF service enabled
   - ZFS pool available

### Step 1: Create Ubuntu VM in UTM

1. **Launch UTM** and click "Create a New Virtual Machine"

2. **Select Virtualize** (not Emulate)

3. **Configure VM:**
   - **Operating System:** Linux
   - **Boot ISO:** Select Ubuntu 22.04 Server ISO
   - **CPU:** 4 cores
   - **Memory:** 4096 MB (4 GB)
   - **Disk:** 50 GB

4. **Network Settings:**
   - **Mode:** Bridged (important for TrueNAS access)
   - **Network Interface:** Select your active adapter

5. **Create and start the VM**

6. **Install Ubuntu:**
   - Follow Ubuntu installer
   - Create user account (remember credentials)
   - Install OpenSSH server (select during installation)
   - Complete installation and reboot

### Step 2: Configure VM for NVMe-oF

SSH into your VM from macOS:

```bash
# Find VM IP (from UTM console)
ip addr show

# SSH from macOS
ssh <username>@<vm-ip>
```

Install required packages:

```bash
# Update system
sudo apt-get update
sudo apt-get upgrade -y

# Install NVMe tools
sudo apt-get install -y nvme-cli curl wget

# Load NVMe-oF kernel modules
sudo modprobe nvme-tcp
sudo modprobe nvme-fabrics

# Verify modules loaded
lsmod | grep nvme

# Make modules load on boot
echo "nvme-tcp" | sudo tee -a /etc/modules
echo "nvme-fabrics" | sudo tee -a /etc/modules
```

Verify NVMe-oF functionality:

```bash
# Test discovery (use your TrueNAS IP)
sudo nvme discover -t tcp -a 10.10.20.100 -s 4420

# Should show available NVMe-oF subsystems
```

### Step 3: Install k3s

```bash
# Install k3s (lightweight Kubernetes)
curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644

# Enable and start k3s
sudo systemctl enable k3s
sudo systemctl start k3s

# Wait for k3s to be ready
sudo kubectl get nodes

# Should show: STATUS=Ready
```

### Step 4: Configure kubectl from macOS

```bash
# Get VM IP
VM_IP=<your-vm-ip>

# Copy kubeconfig from VM
scp <username>@${VM_IP}:/etc/rancher/k3s/k3s.yaml ~/.kube/utm-nvmeof-test

# Update server address to VM IP
sed -i.bak "s|https://127.0.0.1:6443|https://${VM_IP}:6443|g" ~/.kube/utm-nvmeof-test
rm ~/.kube/utm-nvmeof-test.bak

# Test connection from macOS
kubectl --kubeconfig ~/.kube/utm-nvmeof-test get nodes

# Optional: Set as default
export KUBECONFIG=~/.kube/utm-nvmeof-test
kubectl get nodes
```

### Step 5: Deploy CSI Driver

From your macOS development machine:

```bash
# Build CSI driver image
cd /path/to/tns-csi
make build-image

# Save image as tarball
docker save tns-csi-driver:latest | gzip > tns-csi-driver.tar.gz

# Transfer to VM
scp tns-csi-driver.tar.gz <username>@${VM_IP}:~

# Load image into k3s on VM
ssh <username>@${VM_IP} 'sudo k3s ctr images import ~/tns-csi-driver.tar.gz'

# Create TrueNAS credentials secret
export KUBECONFIG=~/.kube/utm-nvmeof-test
kubectl create secret generic tns-csi-secret \
  --namespace kube-system \
  --from-literal=apiKey=<your-truenas-api-key>

# Deploy with Helm
helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --set truenas.host=10.10.20.100 \
  --set image.tag=latest \
  --set image.pullPolicy=Never

# Verify deployment
kubectl get pods -n kube-system | grep tns-csi
```

Expected output:
```
tns-csi-controller-0   3/3     Running   0          1m
tns-csi-node-xxxxx     2/2     Running   0          1m
```

### Step 6: Test NVMe-oF Volume

```bash
export KUBECONFIG=~/.kube/utm-nvmeof-test

# Create NVMe-oF PVC
kubectl apply -f deploy/example-nvmeof-pvc.yaml

# Check PVC status
kubectl get pvc test-nvmeof-pvc

# Should show: STATUS=Bound

# Create test pod
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-nvmeof-pod
  namespace: default
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

# Wait for pod to start
kubectl get pod test-nvmeof-pod -w

# Should show: STATUS=Running

# Verify NVMe device is connected
ssh <username>@${VM_IP} 'sudo nvme list'

# Should show connected NVMe device

# Test I/O operations
kubectl exec test-nvmeof-pod -- dd if=/dev/zero of=/data/test.img bs=1M count=100
kubectl exec test-nvmeof-pod -- ls -lh /data/test.img

# Cleanup
kubectl delete pod test-nvmeof-pod
kubectl delete pvc test-nvmeof-pvc
```

## Kind Cluster Setup for NFS Testing

### Prerequisites

1. **Docker Desktop** running
2. **Kind** installed: `brew install kind`
3. **TrueNAS Scale** server accessible

### Setup and Test

```bash
# Create Kind cluster
kind create cluster --name tns-csi-test

# Build CSI driver
cd /path/to/tns-csi
make build-image

# Load image into Kind
kind load docker-image tns-csi-driver:latest --name tns-csi-test

# Deploy CSI driver
kubectl create secret generic tns-csi-secret \
  --namespace kube-system \
  --from-literal=apiKey=<your-truenas-api-key>

helm install tns-csi ./charts/tns-csi-driver \
  --namespace kube-system \
  --set truenas.host=10.10.20.100 \
  --set image.tag=latest \
  --set image.pullPolicy=Never

# Test NFS volume
kubectl apply -f deploy/example-pvc.yaml
kubectl apply -f deploy/test-pod.yaml

# Verify
kubectl get pvc
kubectl get pod test-nfs-pod

# Cleanup
kubectl delete -f deploy/test-pod.yaml
kubectl delete -f deploy/example-pvc.yaml
```

## Architecture

```
┌────────────────────────────────────────────────────────────────┐
│                        macOS Host                              │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ Development Environment:                                 │  │
│  │ - Code editing                                           │  │
│  │ - Docker builds                                          │  │
│  │ - kubectl to both clusters                              │  │
│  └──────────────────────────────────────────────────────────┘  │
│                          │         │                            │
│                kubectl   │         │   kubectl                  │
│                          ▼         ▼                            │
│  ┌────────────────────────────────────────────────────────┐    │
│  │ UTM VM (Ubuntu 22.04)  │  Kind Cluster (Containers)    │    │
│  │ ┌──────────────────────┤  ┌────────────────────────┐   │    │
│  │ │ k3s Kubernetes       │  │ Kubernetes             │   │    │
│  │ │ - CSI Controller     │  │ - CSI Controller       │   │    │
│  │ │ - CSI Node Plugin    │  │ - CSI Node Plugin      │   │    │
│  │ │ - Test Pods          │  │ - Test Pods            │   │    │
│  │ └──────────────────────┤  └────────────────────────┘   │    │
│  │ ┌──────────────────────┤                                │    │
│  │ │ NVMe-oF Initiator    │  NFS Client in containers     │    │
│  │ │ - nvme-cli           │                                │    │
│  │ │ - nvme-tcp module    │                                │    │
│  │ └──────────────────────┘                                │    │
│  └────────────────────────────────────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
                          │              │
                 NVMe-oF  │              │  NFS
                          ▼              ▼
               ┌──────────────────────────────────┐
               │     TrueNAS Scale Server         │
               │  ┌────────────┬────────────────┐ │
               │  │ NVMe-oF    │  NFS Server    │ │
               │  │ Target     │                │ │
               │  │ (port 4420)│  (port 2049)   │ │
               │  └────────────┴────────────────┘ │
               │  ┌──────────────────────────────┐│
               │  │ ZFS Pools                    ││
               │  │ - Block devices (zvols)      ││
               │  │ - Datasets (NFS shares)      ││
               │  └──────────────────────────────┘│
               └──────────────────────────────────┘
```

## Development Workflow

### Working on NVMe-oF features:

```bash
# 1. Edit code on macOS
vim pkg/driver/node.go

# 2. Build and transfer
make build-image
docker save tns-csi-driver:latest | gzip > tns-csi-driver.tar.gz
scp tns-csi-driver.tar.gz <username>@${VM_IP}:~
ssh <username>@${VM_IP} 'sudo k3s ctr images import ~/tns-csi-driver.tar.gz'

# 3. Restart pods to pick up new image
export KUBECONFIG=~/.kube/utm-nvmeof-test
kubectl rollout restart -n kube-system daemonset/tns-csi-node
kubectl rollout restart -n kube-system statefulset/tns-csi-controller

# 4. Monitor logs
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

# 4. Test immediately
kubectl apply -f deploy/example-pvc.yaml
```

## Troubleshooting

### UTM VM Issues

**Can't SSH into VM:**
```bash
# From UTM console, check IP
ip addr show

# Verify bridged networking in UTM settings
# Ensure macOS firewall allows incoming connections
```

**NVMe modules not loading:**
```bash
ssh <username>@${VM_IP}

# Check module status
lsmod | grep nvme

# Reload modules
sudo modprobe nvme-tcp
sudo modprobe nvme-fabrics

# Check for errors
dmesg | grep -i nvme
```

**Can't connect to TrueNAS:**
```bash
# Test network connectivity
ssh <username>@${VM_IP} 'ping -c 3 10.10.20.100'

# Test NVMe-oF discovery
ssh <username>@${VM_IP} 'sudo nvme discover -t tcp -a 10.10.20.100 -s 4420'

# Verify TrueNAS NVMe-oF service is running
# Check TrueNAS: System Settings → Services → NVMe-oF
```

**kubectl connection fails:**
```bash
# Verify k3s is running on VM
ssh <username>@${VM_IP} 'sudo systemctl status k3s'

# Check VM IP hasn't changed
ssh <username>@${VM_IP} 'ip addr show'

# Regenerate kubeconfig
VM_IP=<new-ip>
scp <username>@${VM_IP}:/etc/rancher/k3s/k3s.yaml ~/.kube/utm-nvmeof-test
sed -i.bak "s|127.0.0.1|${VM_IP}|g" ~/.kube/utm-nvmeof-test
```

**PVC stuck in Pending:**
```bash
export KUBECONFIG=~/.kube/utm-nvmeof-test

# Check PVC events
kubectl describe pvc test-nvmeof-pvc

# Check controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=100

# Verify TrueNAS credentials
kubectl get secret tns-csi-secret -n kube-system -o yaml
```

**Pod stuck in ContainerCreating:**
```bash
export KUBECONFIG=~/.kube/utm-nvmeof-test

# Check pod events
kubectl describe pod test-nvmeof-pod

# Check node driver logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin --tail=100

# Check NVMe devices on VM
ssh <username>@${VM_IP} 'sudo nvme list'

# Manually test NVMe connection
ssh <username>@${VM_IP} 'sudo nvme discover -t tcp -a 10.10.20.100 -s 4420'
```

### Kind Cluster Issues

**Cluster won't start:**
```bash
# Delete and recreate
kind delete cluster --name tns-csi-test
kind create cluster --name tns-csi-test
```

**Image not found:**
```bash
# Reload image
make build-image
kind load docker-image tns-csi-driver:latest --name tns-csi-test

# Verify image is loaded
docker exec tns-csi-test-control-plane crictl images | grep tns-csi
```

**NFS mount fails:**
```bash
# Check TrueNAS NFS service is running
# Verify NFS share was created on TrueNAS
# Check controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin
```

## Manual Testing Commands

### NVMe-oF Operations on VM

```bash
# Connect to VM
ssh <username>@<vm-ip>

# Discover targets
sudo nvme discover -t tcp -a 10.10.20.100 -s 4420

# Connect to subsystem (get NQN from discovery)
sudo nvme connect -t tcp \
  -n nqn.2005-03.org.freenas.ctl:<subsystem-name> \
  -a 10.10.20.100 \
  -s 4420

# List connected devices
sudo nvme list
sudo nvme list-subsys

# Show device details
lsblk
ls -la /dev/nvme*

# Disconnect
sudo nvme disconnect -n nqn.2005-03.org.freenas.ctl:<subsystem-name>
```

### View CSI Driver Logs

```bash
# UTM VM (NVMe-oF testing)
export KUBECONFIG=~/.kube/utm-nvmeof-test

# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin -f

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin -f

# Kind cluster (NFS testing)
kubectl config use-context kind-tns-csi-test

kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin -f
```

## What Gets Tested

### ✅ Fully Tested in This Setup

**NVMe-oF (UTM VM):**
- Volume provisioning (ZVOL creation, subsystem, namespace)
- NVMe-oF target discovery and connection
- Block device mounting in pods
- I/O operations on NVMe devices
- Volume lifecycle (create, mount, unmount, delete)

**NFS (Kind Cluster):**
- NFS share provisioning
- Filesystem mounting in pods
- Standard file operations
- Volume lifecycle

### ⚠️ Requires Multi-Node Setup

- Pod migration between nodes
- Node failure scenarios
- High availability testing

### 📝 Future Work

- Volume snapshots (when implemented)
- Volume cloning (when implemented)
- Volume expansion (when implemented)

## Performance Notes

**UTM VM:**
- ✅ Suitable for functional testing
- ✅ Development and debugging
- ✅ Feature validation
- ❌ Not for performance benchmarking
- ❌ Not for load testing

**Kind Cluster:**
- ✅ Fast iteration
- ✅ Quick functional tests
- ✅ Perfect for NFS testing
- ❌ Not for performance testing

For performance testing, use bare metal or dedicated cloud instances.

## Cleanup

### Temporary (Keep Environment)

```bash
# UTM VM
export KUBECONFIG=~/.kube/utm-nvmeof-test
kubectl delete pod test-nvmeof-pod
kubectl delete pvc test-nvmeof-pvc

# Kind
kubectl delete -f deploy/test-pod.yaml
kubectl delete -f deploy/example-pvc.yaml
```

### Full Cleanup

```bash
# Uninstall CSI driver from UTM VM
export KUBECONFIG=~/.kube/utm-nvmeof-test
helm uninstall tns-csi -n kube-system

# Delete Kind cluster
kind delete cluster --name tns-csi-test

# Stop/Delete UTM VM (through UTM GUI)
# Right-click VM → Stop or Delete
```

## Next Steps

1. **Customize for your environment** - Update IPs, credentials, VM settings
2. **Add automated tests** - Create test scripts for CI/CD
3. **Multi-node testing** - Create multiple VMs to test node migration
4. **Performance testing** - Use bare metal for benchmarks

## Resources

- [UTM Documentation](https://docs.getutm.app/)
- [k3s Documentation](https://docs.k3s.io/)
- [Kind Documentation](https://kind.sigs.k8s.io/)
- [NVMe-CLI Documentation](https://github.com/linux-nvme/nvme-cli)
- [TrueNAS Scale API](https://www.truenas.com/docs/scale/api/)

## Support

For issues with this testing setup:
1. Verify network connectivity between VM/cluster and TrueNAS
2. Check NVMe kernel modules (UTM VM only)
3. Verify TrueNAS services are running
4. Review CSI driver logs

For CSI driver bugs, see main project documentation and GitHub issues.
