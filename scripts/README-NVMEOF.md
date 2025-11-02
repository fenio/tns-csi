# NVMe-oF Testing Scripts

Quick reference for local NVMe-oF testing on macOS.

## Setup (First Time Only)

```bash
# Install Multipass
brew install multipass

# Create and configure test VM (~5 minutes)
./scripts/setup-nvmeof-test-vm.sh
```

This creates an Ubuntu VM with k3s and NVMe-oF support.

## Deploy CSI Driver

```bash
# Build, transfer, and deploy CSI driver (~3 minutes)
./scripts/deploy-nvmeof-test.sh
```

## Run Tests

```bash
# Test NVMe-oF volume provisioning and mounting (~2 minutes)
./scripts/test-nvmeof.sh
```

## Cleanup

```bash
# Interactive cleanup menu
./scripts/cleanup-nvmeof-test.sh
```

Options:
1. Delete test resources only
2. Uninstall CSI driver (keep VM)
3. Stop VM (preserve state)
4. Delete VM completely
5. Full cleanup

## Quick Commands

```bash
# Access VM shell
multipass shell truenas-nvme-test

# Use kubectl from macOS
export KUBECONFIG=~/.kube/k3s-nvmeof-test
kubectl get pods -A

# View CSI driver logs
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin -f
kubectl logs -n kube-system -l app.kubernetes.io/component=node -c tns-csi-plugin -f

# Check NVMe devices in VM
multipass exec truenas-nvme-test -- sudo nvme list

# Restart VM
multipass restart truenas-nvme-test

# Stop VM (saves resources)
multipass stop truenas-nvme-test

# Start stopped VM
multipass start truenas-nvme-test
```

## Development Workflow

1. Edit code on macOS
2. Run `./scripts/deploy-nvmeof-test.sh` to rebuild and deploy
3. Run `./scripts/test-nvmeof.sh` to test changes
4. Check logs if needed
5. Iterate

## Troubleshooting

See [NVMEOF_TESTING.md](../NVMEOF_TESTING.md) for detailed troubleshooting guide.

Quick checks:
```bash
# VM running?
multipass list

# k3s healthy?
multipass exec truenas-nvme-test -- sudo kubectl get nodes

# NVMe modules loaded?
multipass exec truenas-nvme-test -- lsmod | grep nvme

# TrueNAS reachable?
multipass exec truenas-nvme-test -- ping -c 3 10.10.20.100
```

## Files

- `setup-nvmeof-test-vm.sh` - Create Ubuntu VM with k3s and NVMe-oF support
- `deploy-nvmeof-test.sh` - Build and deploy CSI driver to VM
- `test-nvmeof.sh` - Run NVMe-oF test suite
- `cleanup-nvmeof-test.sh` - Clean up test environment

## Full Documentation

See [NVMEOF_TESTING.md](../NVMEOF_TESTING.md) for complete guide including:
- Architecture diagrams
- Manual operations
- Troubleshooting guide
- Development workflow
- Performance notes
