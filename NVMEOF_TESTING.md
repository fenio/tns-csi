# Local NVMe-oF Testing Guide

This guide shows how to test NVMe-oF functionality locally on macOS using Multipass VMs.

## Why This Approach?

**Problem:** macOS (especially Apple Silicon) doesn't support NVMe-oF initiators, iSCSI, or direct block device operations needed for testing storage protocols.

**Solution:** Use Multipass to create an Ubuntu VM with full NVMe-oF support, k3s Kubernetes, and network access to your TrueNAS server.

## Prerequisites

1. **macOS** (Intel or Apple Silicon)
2. **Homebrew** - [Install here](https://brew.sh)
3. **Multipass** - Will be installed in setup
4. **TrueNAS Scale** server accessible on your network
5. **Docker Desktop** - For building images

## Quick Start

### 1. Install Multipass

```bash
brew install multipass
```

### 2. Create and Configure Test VM

```bash
./scripts/setup-nvmeof-test-vm.sh
```

This script will:
- Create Ubuntu 22.04 VM (4 CPU, 4GB RAM, 50GB disk)
- Install NVMe-oF tools (`nvme-cli`)
- Load kernel modules (`nvme-tcp`, `nvme-fabrics`)
- Install k3s (lightweight Kubernetes)
- Configure kubectl access from macOS

**Time:** ~5 minutes

### 3. Deploy CSI Driver

```bash
./scripts/deploy-nvmeof-test.sh
```

This script will:
- Build the CSI driver Docker image
- Transfer image to VM
- Load image into k3s
- Create TrueNAS credentials secret
- Deploy CSI driver with Helm
- Enable both NFS and NVMe-oF storage classes

**Time:** ~3 minutes

### 4. Run Tests

```bash
./scripts/test-nvmeof.sh
```

This script will:
1. Test NFS volume (baseline)
2. Create NVMe-oF PVC
3. Mount NVMe-oF volume in pod
4. Verify NVMe device connection
5. Test I/O operations

**Time:** ~2 minutes

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      macOS Host                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │ You work here:                                  │   │
│  │ - Edit code                                     │   │
│  │ - Run scripts                                   │   │
│  │ - Use kubectl                                   │   │
│  └─────────────────────────────────────────────────┘   │
│                          │                              │
│                          │ kubectl                      │
│                          ▼                              │
│  ┌─────────────────────────────────────────────────┐   │
│  │    Multipass VM (Ubuntu 22.04)                  │   │
│  │  ┌───────────────────────────────────────────┐  │   │
│  │  │ k3s Kubernetes Cluster                    │  │   │
│  │  │  - CSI Controller                         │  │   │
│  │  │  - CSI Node Plugin                        │  │   │
│  │  │  - Test Pods                              │  │   │
│  │  └───────────────────────────────────────────┘  │   │
│  │  ┌───────────────────────────────────────────┐  │   │
│  │  │ NVMe-oF Initiator                         │  │   │
│  │  │  - nvme-cli                               │  │   │
│  │  │  - nvme-tcp kernel module                 │  │   │
│  │  └───────────────────────────────────────────┘  │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
                          │
                          │ Network
                          ▼
            ┌─────────────────────────────┐
            │   TrueNAS Scale Server      │
            │  - ZFS Pools                │
            │  - NVMe-oF Target           │
            │  - NFS Server               │
            │  - WebSocket API            │
            └─────────────────────────────┘
```

## Manual Operations

### Access VM Shell

```bash
multipass shell truenas-nvme-test
```

### View NVMe Devices

```bash
# From macOS
multipass exec truenas-nvme-test -- sudo nvme list

# Or from inside VM
multipass shell truenas-nvme-test
sudo nvme list
sudo nvme list-subsys
```

### Use kubectl from macOS

```bash
# Option 1: Specify kubeconfig each time
kubectl --kubeconfig ~/.kube/k3s-nvmeof-test get pods -A

# Option 2: Set as default
export KUBECONFIG=~/.kube/k3s-nvmeof-test
kubectl get pods -A
```

### View CSI Driver Logs

```bash
export KUBECONFIG=~/.kube/k3s-nvmeof-test

# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin -f

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin -f
```

### Test NVMe-oF Connection Manually

```bash
# Shell into VM
multipass shell truenas-nvme-test

# Discover NVMe-oF targets
sudo nvme discover -t tcp -a 10.10.20.100 -s 4420

# Connect to a specific NQN (get from TrueNAS)
sudo nvme connect -t tcp -n nqn.2005-03.org.freenas.ctl:test-subsystem -a 10.10.20.100 -s 4420

# List connected devices
sudo nvme list

# Disconnect
sudo nvme disconnect -n nqn.2005-03.org.freenas.ctl:test-subsystem
```

## Troubleshooting

### VM Won't Start

```bash
# Check Multipass status
multipass list

# View VM logs
multipass info truenas-nvme-test

# Restart VM
multipass restart truenas-nvme-test
```

### Can't Connect with kubectl

```bash
# Verify VM is running
multipass list

# Check VM IP
multipass info truenas-nvme-test | grep IPv4

# Regenerate kubeconfig
VM_IP=$(multipass info truenas-nvme-test | grep IPv4 | awk '{print $2}')
multipass exec truenas-nvme-test -- sudo cat /etc/rancher/k3s/k3s.yaml > ~/.kube/k3s-nvmeof-test
sed -i.bak "s|127.0.0.1|${VM_IP}|g" ~/.kube/k3s-nvmeof-test
```

### NVMe-oF Connection Fails

```bash
# Check if kernel modules are loaded
multipass exec truenas-nvme-test -- lsmod | grep nvme

# Reload modules
multipass exec truenas-nvme-test -- sudo modprobe nvme-tcp
multipass exec truenas-nvme-test -- sudo modprobe nvme-fabrics

# Check TrueNAS connectivity
multipass exec truenas-nvme-test -- ping -c 3 10.10.20.100

# Test NVMe-oF discovery
multipass exec truenas-nvme-test -- sudo nvme discover -t tcp -a 10.10.20.100 -s 4420
```

### PVC Stuck in Pending

```bash
export KUBECONFIG=~/.kube/k3s-nvmeof-test

# Check PVC events
kubectl describe pvc test-nvmeof-pvc

# Check controller logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=100

# Verify TrueNAS credentials
kubectl get secret tns-csi-secret -n kube-system -o yaml
```

### Pod Stuck in ContainerCreating

```bash
export KUBECONFIG=~/.kube/k3s-nvmeof-test

# Check pod events
kubectl describe pod test-nvmeof-pod

# Check node logs
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin --tail=100

# Check NVMe devices in VM
multipass exec truenas-nvme-test -- sudo nvme list
```

## Development Workflow

1. **Edit code on macOS** in your favorite editor
2. **Test changes:**
   ```bash
   ./scripts/deploy-nvmeof-test.sh  # Rebuilds and redeploys
   ./scripts/test-nvmeof.sh         # Runs test suite
   ```
3. **Debug if needed:**
   ```bash
   export KUBECONFIG=~/.kube/k3s-nvmeof-test
   kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin -f
   ```
4. **Iterate** - repeat as needed

## Cleanup

### Delete Test Resources

```bash
export KUBECONFIG=~/.kube/k3s-nvmeof-test

kubectl delete pod test-nvmeof-pod
kubectl delete pvc test-nvmeof-pvc
```

### Uninstall CSI Driver

```bash
helm --kubeconfig ~/.kube/k3s-nvmeof-test uninstall tns-csi -n kube-system
```

### Stop VM (Preserves State)

```bash
multipass stop truenas-nvme-test
```

### Delete VM Completely

```bash
multipass delete truenas-nvme-test
multipass purge
```

## Cost Comparison

| Method | Setup Time | Test Time | Monthly Cost | Pros |
|--------|------------|-----------|--------------|------|
| **Multipass VM** | 5 min | < 1 min | $0 | Fast, local, free |
| Cloud VM (always on) | 10 min | < 1 min | $5-20 | Remote access, CI/CD ready |
| Cloud VM (on-demand) | 15 min | 2 min | $1-5 | Only pay when testing |
| Bare Metal Linux | 30 min | < 1 min | $0 | Best performance, permanent |

## What Gets Tested

### ✅ Fully Tested in This Setup

- NVMe-oF volume provisioning (ZVOL creation, subsystem, namespace)
- NVMe-oF target connection
- Block device mounting in pods
- I/O operations on NVMe devices
- NFS volumes (baseline comparison)
- Multi-protocol support
- Volume lifecycle (create, mount, unmount, delete)

### ⚠️ Not Tested (Requires Multiple Nodes)

- Pod migration between nodes
- Multi-attach (ReadWriteMany for block storage - not supported by protocol)
- Node failure scenarios

### 📝 Requires Manual Testing

- Volume snapshots (when implemented)
- Volume cloning (when implemented)
- Volume expansion (when implemented)

## Performance Notes

The Multipass VM is suitable for:
- ✅ Functional testing
- ✅ Development and debugging
- ✅ CI/CD integration
- ✅ Feature validation

The VM is **NOT** suitable for:
- ❌ Performance benchmarking
- ❌ Load testing
- ❌ Production workloads

For performance testing, use real hardware or dedicated cloud instances.

## Next Steps

After successful local testing:

1. **Extend tests** - Add more test scenarios to `test-nvmeof.sh`
2. **CI/CD integration** - Adapt scripts for GitHub Actions with self-hosted runner
3. **Multi-node testing** - Create multiple VMs to test node migration
4. **Automation** - Add to your development workflow

## Resources

- [Multipass Documentation](https://multipass.run/docs)
- [k3s Documentation](https://docs.k3s.io/)
- [NVMe-CLI Documentation](https://github.com/linux-nvme/nvme-cli)
- [TrueNAS Scale API](https://www.truenas.com/docs/scale/api/)

## Support

Issues with this testing setup? Check:
1. VM has network connectivity to TrueNAS
2. NVMe kernel modules are loaded
3. TrueNAS NVMe-oF target is configured and running
4. CSI driver pods are running in k3s

For CSI driver bugs, see main project documentation.
