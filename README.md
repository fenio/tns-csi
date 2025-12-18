# TNS CSI Driver

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.25.4-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/fenio/tns-csi/actions/workflows/ci.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/ci.yml)
[![Integration Tests](https://github.com/fenio/tns-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/integration.yml)
[![Distro Compatibility](https://github.com/fenio/tns-csi/actions/workflows/distro-compatibility.yml/badge.svg)](https://github.com/fenio/tns-csi/actions/workflows/distro-compatibility.yml)
[![Test Dashboard](https://img.shields.io/badge/Test%20Dashboard-View-blue)](https://fenio.github.io/tns-csi/dashboard/)
[![Docker Hub](https://img.shields.io/docker/pulls/bfenski/tns-csi?logo=docker)](https://hub.docker.com/r/bfenski/tns-csi)
[![Release](https://img.shields.io/github/v/release/fenio/tns-csi?logo=github)](https://github.com/fenio/tns-csi/releases/latest)

A Kubernetes CSI (Container Storage Interface) driver for TrueNAS Scale 25.10+.

## Important Disclaimer

**This project is in early development phase and is NOT production-ready**
- Use of this software is entirely at your own risk
- Extensive testing and validation required before production use

## Overview

This CSI driver enables Kubernetes to provision and manage persistent volumes on TrueNAS Scale 25.10+. It currently supports:

- **NFS** - Network File System for file-based storage
- **NVMe-oF** - NVMe over Fabrics for high-performance block storage

### Why NFS and NVMe-oF?

This driver focuses on these two protocols for specific reasons:

- **NVMe-oF over iSCSI**: NVMe-oF provides superior performance with lower latency and higher IOPS compared to iSCSI. It's designed for modern NVMe SSDs and takes full advantage of their capabilities. For block storage workloads, NVMe-oF is the clear choice.
- **SMB protocol**: Currently has low priority due to author's preference for Linux-native protocols. If you need Windows file sharing support, consider the Democratic-CSI driver or contribute SMB support if there's sufficient community demand.

The driver intentionally focuses on these two production-ready protocols rather than spreading development effort across multiple less-optimal options.

## Features

- **Dynamic volume provisioning** - Automatically create and delete storage volumes
- **Multiple protocol support** - NFS for file storage, NVMe-oF for high-performance block storage
- **Volume lifecycle management** - Full create, delete, attach, detach, mount, unmount operations
- **Volume snapshots** - Create, delete, and restore from snapshots (NFS and NVMe-oF)
- **Volume cloning** - Create new volumes from existing snapshots
- **Volume expansion** - Resize volumes dynamically (supported for both NFS and NVMe-oF)
- **Volume retention** - Optional `deleteStrategy: retain` to keep volumes on PVC deletion
- **Configurable mount options** - Customize NFS/NVMe-oF mount options via StorageClass
- **Configurable ZFS properties** - Set compression, dedup, recordsize, etc. via StorageClass parameters
- **Access modes** - ReadWriteOnce (RWO) and ReadWriteMany (RWX) support
- **Storage classes** - Flexible configuration via Kubernetes storage classes
- **Connection resilience** - Automatic reconnection with exponential backoff for WebSocket API

## Kubernetes Distribution Compatibility

This driver is tested and verified to work on **6 Kubernetes distributions** with both NFS and NVMe-oF protocols:

| Distribution | NFS | NVMe-oF | Description |
|--------------|:---:|:-------:|-------------|
| K3s | ✅ | ✅ | Lightweight Kubernetes by Rancher |
| K0s | ✅ | ✅ | Zero-friction Kubernetes by Mirantis |
| KubeSolo | ✅ | ✅ | Single-node Kubernetes |
| Minikube | ✅ | ✅ | Local Kubernetes for development |
| Talos | ✅ | ✅ | Secure, immutable Kubernetes OS |
| MicroK8s | ✅ | ✅ | Lightweight Kubernetes by Canonical |

Compatibility tests run weekly and on-demand. See [Distro Compatibility Tests](docs/DISTRO-COMPATIBILITY.md) for details.

## Prerequisites

- Kubernetes 1.27+ (earlier versions may work but are not tested)
- **TrueNAS Scale 25.10 or later** (required for full feature support including NVMe-oF)
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu, `nfs-utils` on RHEL/CentOS)
- For NVMe-oF: 
  - TrueNAS Scale 25.10+
  - **TrueNAS must have a static IP configured** (DHCP not supported for NVMe-oF)
  - At least one NVMe-oF subsystem with:
    - Initial ZVOL namespace configured
    - TCP port configured (default: 4420)
  - `nvme-cli` package installed on all Kubernetes nodes
  - Kernel modules: `nvme-tcp`, `nvme-fabrics`
  - Network connectivity from Kubernetes nodes to TrueNAS on port 4420

## Quick Start

See [DEPLOYMENT.md](docs/DEPLOYMENT.md) for detailed installation and configuration instructions.

### Installation via Helm (Recommended)

The TNS CSI Driver is published to both Docker Hub and GitHub Container Registry as OCI artifacts:

#### Docker Hub (recommended)
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

**NVMe-oF Example:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.subsystemNQN="nqn.2025-01.com.truenas:csi" \
  --set storageClasses.nvmeof.transport=tcp \
  --set storageClasses.nvmeof.port=4420
```

**Note:** Replace `nqn.2025-01.com.truenas:csi` with your actual NVMe-oF subsystem NQN. You must pre-configure the subsystem in TrueNAS (Shares > NVMe-oF Subsystems) before provisioning volumes.

See the [Helm chart README](charts/tns-csi-driver/README.md) for detailed configuration options.

<details>
<summary>Manual Installation (kubectl) - Click to expand</summary>

For advanced users who prefer manual deployment without Helm:

1. Create namespace and RBAC:
```bash
kubectl apply -f deploy/rbac.yaml
```

2. Configure TrueNAS credentials:
```bash
# Copy the example secret file and edit with your actual credentials
cp deploy/secret.yaml deploy/secret.local.yaml
# Edit deploy/secret.local.yaml with your TrueNAS IP and API key
kubectl apply -f deploy/secret.local.yaml
```

**Note:** The files in the `deploy/` directory contain placeholder values. Create `*.local.yaml` versions with your actual configuration. These local files are automatically ignored by git.

3. Deploy the CSI driver:
```bash
kubectl apply -f deploy/csidriver.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

4. Create a storage class:
```bash
kubectl apply -f deploy/storageclass.yaml
```

</details>

## Configuration

The driver is configured via command-line flags and Kubernetes secrets:

### Command-Line Flags

- `--endpoint` - CSI endpoint (default: `unix:///var/lib/kubelet/plugins/tns.csi.io/csi.sock`)
- `--node-id` - Node identifier (typically the node name)
- `--driver-name` - CSI driver name (default: `tns.csi.io`)
- `--api-url` - TrueNAS API URL (e.g., `ws://YOUR-TRUENAS-IP/api/v2.0/websocket`)
- `--api-key` - TrueNAS API key

### Storage Class Parameters

**NFS Volumes:**
```yaml
parameters:
  protocol: nfs
  server: YOUR-TRUENAS-IP
  pool: tank
  path: /mnt/tank/k8s
```

**NVMe-oF Volumes:**
```yaml
parameters:
  protocol: nvmeof
  server: YOUR-TRUENAS-IP
  pool: tank
  subsystemNQN: nqn.2025-01.com.truenas:csi  # REQUIRED: Pre-configured subsystem NQN
  path: /mnt/tank/k8s/nvmeof
  fsType: ext4  # or xfs
```

**Important:** The `subsystemNQN` parameter is required and must match a pre-configured NVMe-oF subsystem in TrueNAS (Shares > NVMe-oF Subsystems). The CSI driver creates namespaces within this shared subsystem for each volume.

## Testing

**Comprehensive Testing on Real Infrastructure**

This driver is tested extensively using **real hardware and software** - not mocks or simulators:

- **Self-hosted GitHub Actions runner** on dedicated OVH infrastructure
- **Real Kubernetes cluster** (k3s) provisioned for each test run
- **Real TrueNAS Scale server** with actual storage pools and network services on dedicated sponsored by Akamai/Linode infrastructure
- **Full protocol stack testing** - NFS mounts, NVMe-oF connections, actual I/O operations

### Automated Test Suite

Every commit triggers comprehensive integration tests:

**Core Functionality Tests:**
- Basic volume provisioning and deletion (NFS & NVMe-oF)
- Volume expansion (dynamic resizing)
- Snapshot creation and restoration
- Volume cloning from snapshots
- StatefulSet volume management
- Data persistence across pod restarts

**Stress & Reliability Tests:**
- Concurrent volume creation (5 simultaneous volumes)
- Connection resilience (WebSocket reconnection)
- Orphaned resource detection and cleanup

**CSI Specification Compliance:**
- Passes [Kubernetes CSI sanity tests](https://github.com/kubernetes-csi/csi-test) (v5.4.0)
- Full CSI spec compliance verified

View test results and history: [![Test Dashboard](https://img.shields.io/badge/Test%20Dashboard-View-blue)](https://fenio.github.io/tns-csi/dashboard/)

## Project Status and Limitations

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This driver is in early development and requires extensive testing before production use. Key considerations:

- **Development Phase**: Active development with ongoing testing and validation
- **Protocol Support**: Currently supports NFS and NVMe-oF. iSCSI and SMB may be considered for future releases.
- **Volume Expansion**: Implemented and functional for both NFS and NVMe-oF protocols when `allowVolumeExpansion: true` is set in the StorageClass (Helm chart enables this by default)
- **Snapshots**: Implemented for both NFS and NVMe-oF protocols, functional and tested
- **Testing**: Comprehensive automated testing on real infrastructure (see Testing section above)
- **Stability**: Core features functional but may have undiscovered edge cases or bugs

**Recommended Use**: Development, testing, and evaluation environments only. Use at your own risk.

## Troubleshooting

See [DEPLOYMENT.md](docs/DEPLOYMENT.md#troubleshooting) for detailed troubleshooting steps.

**Common Issues:**

1. **Pods stuck in ContainerCreating**: 
   - For NFS: Check that NFS client utilities are installed on nodes
   - For NVMe-oF: Check that nvme-cli is installed and kernel modules are loaded
2. **Failed to create volume**: Verify storage API credentials and network connectivity
3. **Mount failed**: 
   - For NFS: Ensure NFS service is running on TrueNAS and accessible from nodes
   - For NVMe-oF: Ensure NVMe-oF service is enabled and firewall allows port 4420

**View Logs:**

For Helm deployments:
```bash
# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node
```

For manual (kubectl) deployments:
```bash
# Controller logs
kubectl logs -n kube-system -l app=tns-csi,component=controller

# Node logs
kubectl logs -n kube-system -l app=tns-csi,component=node
```

## Documentation

- [Features Documentation](docs/FEATURES.md) - Comprehensive feature support reference
- [Deployment Guide](docs/DEPLOYMENT.md) - Detailed installation and configuration
- [Quick Start - NFS](docs/QUICKSTART.md) - Get started with NFS volumes
- [Quick Start - NVMe-oF](docs/QUICKSTART-NVMEOF.md) - Get started with NVMe-oF volumes
- [Snapshots Guide](docs/SNAPSHOTS.md) - Volume snapshots and cloning
- [Distro Compatibility](docs/DISTRO-COMPATIBILITY.md) - Kubernetes distribution compatibility testing
- [Metrics Guide](docs/METRICS.md) - Prometheus metrics and monitoring
- [Kind Setup](docs/KIND.md) - Local development with Kind
- [Security](docs/SECURITY-SANITIZATION.md) - Security considerations
- [Comparison with Democratic-CSI](docs/COMPARISON.md) - Feature comparison with democratic-csi

## Development

### Prerequisites

- Go 1.21+
- Docker (for building images)
- Kubernetes cluster for testing

### Building

```bash
make build
```

### Testing

Tests are automated via GitHub Actions CI/CD running on self-hosted infrastructure with real TrueNAS hardware. See `.github/workflows/` for workflow configuration.

**Local Testing:**
```bash
# Run unit tests
make test

# Run specific test
go test -v ./pkg/driver/...

# Run CSI sanity tests (requires TrueNAS connection)
cd tests/sanity && ./test-sanity.sh
```

See the Testing section above for details on the comprehensive integration test suite.

### Building Container Image

```bash
make docker-build
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

This project is licensed under the GNU General Public License v3.0 (GPL-3.0) - see the LICENSE file for details.

## Acknowledgments

This driver is designed to work with TrueNAS Scale 25.10+.
