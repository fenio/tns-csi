# Self-Hosted GitHub Actions Runner Setup

This guide explains how to set up a self-hosted GitHub Actions runner for the TNS CSI Driver CI/CD pipeline. The runner needs to support both NFS and NVMe-oF testing, which requires specific kernel modules and a Kubernetes cluster.

## Overview

**Why Self-Hosted Runners?**
- NVMe-oF testing requires kernel modules (`nvme-tcp`, `nvme-fabrics`) not available on GitHub-hosted runners
- Need direct network access to TrueNAS for integration testing
- Better control over test environment and dependencies

**Architecture:**
```
GitHub (Private Repo)
    ↓
Self-Hosted Runner (Ubuntu VM)
    ├── k3s Kubernetes cluster
    ├── NVMe-oF kernel modules
    └── Wireguard VPN → TrueNAS
```

## Prerequisites

- Ubuntu 22.04 LTS or later (bare metal or VM)
- Minimum 4 CPU cores
- Minimum 8GB RAM (16GB recommended)
- 50GB+ disk space
- Sudo/root access
- Network connectivity to GitHub
- Private GitHub repository (CRITICAL for security)

## Why Private Repository?

**SECURITY WARNING:** Self-hosted runners on **public repositories are unsafe**. Anyone can fork your repo and submit a malicious PR that executes arbitrary code on your runner.

**Always use:**
- Private GitHub repository
- Restrict PR permissions
- Review all PRs before allowing CI execution

## Installation Steps

### Step 1: System Preparation

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install required packages
sudo apt install -y \
    curl \
    wget \
    git \
    build-essential \
    apt-transport-https \
    ca-certificates \
    software-properties-common \
    nvme-cli \
    nfs-common

# Verify NVMe-oF kernel modules
sudo modprobe nvme-tcp
sudo modprobe nvme-fabrics
lsmod | grep nvme

# Make modules load on boot
echo "nvme-tcp" | sudo tee -a /etc/modules
echo "nvme-fabrics" | sudo tee -a /etc/modules
```

### Step 2: Install Docker

The runner needs Docker for building container images:

```bash
# Install Docker
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh

# Add user to docker group
sudo usermod -aG docker $USER

# Enable and start Docker
sudo systemctl enable docker
sudo systemctl start docker

# Verify
docker --version
```

### Step 3: Install k3s

We use k3s (lightweight Kubernetes) for testing:

```bash
# Install k3s
curl -sfL https://get.k3s.io | sh -s - \
    --write-kubeconfig-mode 644 \
    --disable traefik \
    --disable servicelb

# Verify installation
sudo k3s kubectl get nodes

# Configure kubectl for current user
mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $USER:$USER ~/.kube/config

# Install kubectl (if not already installed)
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl

# Verify
kubectl get nodes
```

### Step 4: Install Go

Required for running Go tests:

```bash
# Download and install Go 1.25+
GO_VERSION=1.25.4
wget https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
rm go${GO_VERSION}.linux-amd64.tar.gz

# Add to PATH (add to ~/.bashrc for persistence)
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc

# Verify
go version
```

### Step 5: Install Helm

For deploying the CSI driver:

```bash
# Install Helm
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

# Verify
helm version
```

### Step 6: Install GitHub Actions Runner

**Note:** You'll need to generate a runner token from your GitHub repository settings.

1. **Get Runner Token:**
   - Go to your GitHub repository
   - Settings → Actions → Runners → New self-hosted runner
   - Follow instructions to get the token

2. **Install Runner:**

```bash
# Create runner directory
mkdir -p ~/actions-runner && cd ~/actions-runner

# Download latest runner
curl -o actions-runner-linux-x64-2.311.0.tar.gz -L \
    https://github.com/actions/runner/releases/download/v2.311.0/actions-runner-linux-x64-2.311.0.tar.gz

# Extract
tar xzf ./actions-runner-linux-x64-2.311.0.tar.gz

# Configure runner (use your actual repository and token)
./config.sh --url https://github.com/yourusername/tns-csi --token YOUR_RUNNER_TOKEN

# Add labels for targeting specific workflows
./config.sh --url https://github.com/yourusername/tns-csi \
    --token YOUR_RUNNER_TOKEN \
    --labels linux,x64,nvmeof,nfs

# Install as systemd service
sudo ./svc.sh install
sudo ./svc.sh start

# Check status
sudo ./svc.sh status
```

### Step 7: Configure Runner as Systemd Service

Create a more robust systemd service:

```bash
# Create service file
sudo tee /etc/systemd/system/github-runner.service > /dev/null <<EOF
[Unit]
Description=GitHub Actions Runner
After=network.target

[Service]
Type=simple
User=$USER
WorkingDirectory=$HOME/actions-runner
ExecStart=$HOME/actions-runner/run.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable github-runner
sudo systemctl start github-runner

# Check status
sudo systemctl status github-runner
```

## Runner Configuration

### Environment Variables

The runner needs access to TrueNAS for testing. These should be configured as **GitHub repository secrets**, not on the runner itself:

**Required Secrets (Set in GitHub):**
- `TRUENAS_HOST` - TrueNAS IP/hostname
- `TRUENAS_API_KEY` - API key for TrueNAS
- `TRUENAS_POOL` - Storage pool name
- `TRUENAS_NFS_SHARE_PATH` - Base path for NFS shares
- `TRUENAS_NVMEOF_PATH` - Base path for NVMe-oF namespaces

### Runner Labels

Configure labels to target specific jobs:

```bash
# Re-configure with labels
cd ~/actions-runner
./config.sh remove --token YOUR_TOKEN
./config.sh --url https://github.com/yourusername/tns-csi \
    --token YOUR_NEW_TOKEN \
    --labels self-hosted,linux,x64,k3s,nvmeof,nfs
```

## Networking Configuration

**Network Requirements:**
- Outbound HTTPS (443) to GitHub
- Access to TrueNAS:
  - Port 443 (API/WebSocket)
  - Port 2049 (NFS)
  - Port 4420 (NVMe-oF)

If your TrueNAS is on a private network, configure appropriate VPN or network routing to allow the runner to reach TrueNAS.

## Maintenance

### Updates

```bash
# Update runner
cd ~/actions-runner
sudo ./svc.sh stop
./config.sh remove --token YOUR_TOKEN
# Download new version and reconfigure
sudo ./svc.sh start

# Update system packages
sudo apt update && sudo apt upgrade -y

# Update k3s
curl -sfL https://get.k3s.io | sh -
```

### Monitoring

```bash
# Check runner status
sudo systemctl status github-runner

# View runner logs
journalctl -u github-runner -f

# Check k3s status
kubectl get nodes
kubectl get pods -A

# Monitor resources
htop
df -h
```

### Cleanup

```bash
# Clean up old test artifacts
kubectl delete ns test-* --all

# Clean Docker images
docker system prune -af

# Clean k3s resources
sudo k3s kubectl delete pods --all -A --force --grace-period=0
```

## Troubleshooting

### Runner Not Starting

```bash
# Check service logs
journalctl -u github-runner -n 50

# Check runner directory permissions
ls -la ~/actions-runner

# Manually test runner
cd ~/actions-runner
./run.sh
```

### NVMe-oF Module Issues

```bash
# Verify modules are loaded
lsmod | grep nvme

# Load manually
sudo modprobe nvme-tcp
sudo modprobe nvme-fabrics

# Check kernel support
ls /sys/module/nvme*
```

### k3s Issues

```bash
# Restart k3s
sudo systemctl restart k3s

# Check k3s logs
sudo journalctl -u k3s -f

# Reset k3s (WARNING: destroys all data)
/usr/local/bin/k3s-uninstall.sh
# Then reinstall
```

### Network Connectivity

```bash
# Test GitHub connectivity
curl -I https://github.com

# Test TrueNAS API (via Wireguard)
curl -k https://TRUENAS_IP:443/api/v2.0/system/info

# Check Wireguard status
sudo wg show
```

## Security Considerations

1. **Isolate Runner**: Run on dedicated VM/host
2. **Firewall**: Restrict inbound connections
3. **Updates**: Keep system and packages updated
4. **Monitoring**: Set up alerts for unusual activity
5. **Secrets**: Never commit credentials, use GitHub secrets
6. **Private Repo**: Required for safe self-hosted runner use

## Next Steps

1. [Set up GitHub Actions workflows](../.github/workflows/)
2. Configure repository secrets in GitHub Settings → Secrets and Variables → Actions

## References

- [GitHub Self-Hosted Runners](https://docs.github.com/en/actions/hosting-your-own-runners)
- [k3s Documentation](https://docs.k3s.io/)
- [NVMe-oF Linux](https://github.com/linux-nvme/nvme-cli)
