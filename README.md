# TNS CSI Driver

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.25.7-00ADD8?logo=go)](https://go.dev/)
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
- **iSCSI** - Traditional block storage protocol with broad compatibility

## Comparison with Other Drivers

| | TNS-CSI | truenas-csi (Official) | Democratic-CSI |
|---|---------|------------------------|----------------|
| **Best for** | Modern TrueNAS with NVMe-oF | Scheduled snapshots, CHAP auth | Broad compatibility |
| **Block protocols** | NVMe-oF, iSCSI | iSCSI | iSCSI (+ NVMe-oF for ZoL) |
| **Unique strength** | kubectl plugin, metrics, adoption, encryption | Scheduled snapshots | Multi-backend, Windows |
| **Trade-off** | WebSocket API only | No NVMe-oF, no plugin | SSH complexity |
| **Maturity** | Early development | Very new (Dec 2025) | Mature, production-ready |

See detailed comparisons:
- [TNS-CSI vs truenas-csi (Official)](docs/COMPARISON-TRUENAS-CSI.md)
- [TNS-CSI vs Democratic-CSI](docs/COMPARISON-DEMOCRATIC-CSI.md)

## Dashboard

<img width="1380" height="914" alt="image" src="https://github.com/user-attachments/assets/5d2ce624-2031-442d-8f6f-5422bce9bab7" />

### Protocol Selection Guide

This driver supports three storage protocols:

- **NFS**: Best for shared file storage where multiple pods need concurrent access (ReadWriteMany)
- **NVMe-oF**: Best for high-performance block storage with lowest latency and highest IOPS - ideal for databases and latency-sensitive workloads
- **iSCSI**: Traditional block storage with broad compatibility - useful when NVMe-oF is not available or for environments already using iSCSI

**Note:** SMB/CIFS is not supported due to the Linux-focused nature of this driver. If you need Windows file sharing, consider the Democratic-CSI driver.

## Features

- **Dynamic volume provisioning** - Automatically create and delete storage volumes
- **Multiple protocol support** - NFS for file storage, NVMe-oF and iSCSI for block storage
- **Volume lifecycle management** - Full create, delete, attach, detach, mount, unmount operations
- **Volume snapshots** - Create, delete, and restore from snapshots (NFS, NVMe-oF, and iSCSI)
- **Volume cloning** - Create new volumes from existing snapshots
- **Volume expansion** - Resize volumes dynamically (NFS, NVMe-oF, and iSCSI)
- **Volume retention** - Optional `deleteStrategy: retain` to keep volumes on PVC deletion
- **Volume adoption** - Automatically adopt orphaned volumes for GitOps and disaster recovery workflows (see [Adoption Guide](docs/ADOPTION.md))
- **Configurable mount options** - Customize NFS/NVMe-oF/iSCSI mount options via StorageClass
- **Configurable ZFS properties** - Set compression, dedup, recordsize, etc. via StorageClass parameters
- **Access modes** - ReadWriteOnce (RWO), ReadWriteOncePod (RWOP), and ReadWriteMany (RWX) support
- **Storage classes** - Flexible configuration via Kubernetes storage classes
- **Connection resilience** - Automatic reconnection with exponential backoff for WebSocket API

## kubectl Plugin

The project includes a kubectl plugin (`kubectl tns-csi`) for managing volumes directly from the command line:

```bash
# Install via krew (recommended)
kubectl krew install tns-csi

# Or download from GitHub releases
```

**Key Commands:**
| Command | Description |
|---------|-------------|
| `kubectl tns-csi summary` | Dashboard overview of all resources |
| `kubectl tns-csi list` | List all managed volumes |
| `kubectl tns-csi list-snapshots` | List snapshots with source volumes |
| `kubectl tns-csi health` | Check health of all volumes |
| `kubectl tns-csi troubleshoot <pvc>` | Diagnose PVC issues |
| `kubectl tns-csi cleanup` | Delete orphaned volumes |
| `kubectl tns-csi serve` | Start web dashboard on http://localhost:8080 |

The plugin **auto-discovers credentials** from the installed driver, so it works out of the box on clusters with tns-csi installed.

See [kubectl Plugin Documentation](docs/KUBECTL-PLUGIN.md) for full details.

## Kubernetes Distribution Compatibility

This driver is tested and verified to work on **6 Kubernetes distributions** with NFS, NVMe-oF, and iSCSI protocols:

| Distribution | NFS | NVMe-oF | iSCSI | Description |
|--------------|:---:|:-------:|:-----:|-------------|
| K3s | ✅ | ✅ | ✅ | Lightweight Kubernetes by Rancher |
| K0s | ✅ | ✅ | ✅ | Zero-friction Kubernetes by Mirantis |
| KubeSolo | ✅ | ✅ | ✅ | Single-node Kubernetes |
| Minikube | ✅ | ✅ | ✅ | Local Kubernetes for development |
| Talos | ✅ | ✅ | ✅ | Secure, immutable Kubernetes OS |
| MicroK8s | ✅ | ✅ | ✅ | Lightweight Kubernetes by Canonical |

Compatibility tests run weekly and on-demand. See [Distro Compatibility Tests](docs/DISTRO-COMPATIBILITY.md) for details.

## Prerequisites

- Kubernetes 1.27+ (earlier versions may work but are not tested)
- **TrueNAS Scale 25.10 or later** (required for full feature support including NVMe-oF)
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu, `nfs-utils` on RHEL/CentOS)
- For NVMe-oF:
  - TrueNAS Scale 25.10+
  - **TrueNAS must have a static IP configured** (DHCP not supported for NVMe-oF)
  - At least one NVMe-oF TCP port configured in TrueNAS (Shares > NVMe-oF Targets > Ports, default: 4420)
  - `nvme-cli` package installed on all Kubernetes nodes
  - Kernel modules: `nvme-tcp`, `nvme-fabrics`
  - Network connectivity from Kubernetes nodes to TrueNAS on port 4420
- For iSCSI:
  - TrueNAS Scale 25.10+
  - iSCSI service enabled in TrueNAS (System > Services > iSCSI)
  - `open-iscsi` package installed on all Kubernetes nodes (`iscsid` service running)
  - Network connectivity from Kubernetes nodes to TrueNAS on port 3260

## Quick Start

See [DEPLOYMENT.md](docs/DEPLOYMENT.md) for detailed installation and configuration instructions.

### Installation via Helm (Recommended)

The TNS CSI Driver is published to both Docker Hub and GitHub Container Registry as OCI artifacts:

**Always use a specific version in production.** See [docs/VERSIONING.md](docs/VERSIONING.md) for details.

#### Docker Hub (recommended)
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.2 \
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
  --version 0.9.2 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.pool="YOUR-POOL-NAME" \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nvmeof.transport=tcp \
  --set storageClasses.nvmeof.port=4420
```

**Note:** NVMe-oF requires a TCP port to be pre-configured in TrueNAS (Shares > NVMe-oF Targets > Ports). Subsystems are automatically created per volume.

**iSCSI Example:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.2 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.iscsi.enabled=true \
  --set storageClasses.iscsi.pool="YOUR-POOL-NAME" \
  --set storageClasses.iscsi.server="YOUR-TRUENAS-IP"
```

**Note:** iSCSI requires the iSCSI service to be enabled in TrueNAS (System > Services). Targets and extents are automatically created per volume.

See the [Helm chart README](charts/tns-csi-driver/README.md) for detailed configuration options.

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
  path: /mnt/tank/k8s/nvmeof
  fsType: ext4  # or xfs
```

**Note:** Subsystems are automatically created per volume. Ensure an NVMe-oF TCP port is configured in TrueNAS (Shares > NVMe-oF Targets > Ports).

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
- Basic volume provisioning and deletion (NFS, NVMe-oF & iSCSI)
- Volume expansion (dynamic resizing)
- Snapshot creation and restoration
- Volume cloning from snapshots
- Volume adoption (GitOps workflows)
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
- **Protocol Support**: Currently supports NFS, NVMe-oF, and iSCSI. SMB is not planned (Linux-focused driver).
- **Volume Expansion**: Implemented and functional for NFS, NVMe-oF, and iSCSI protocols when `allowVolumeExpansion: true` is set in the StorageClass (Helm chart enables this by default)
- **Snapshots**: Implemented for NFS, NVMe-oF, and iSCSI protocols, functional and tested
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

```bash
# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node

# Check version
kubectl logs -n kube-system deployment/tns-csi-controller 2>&1 | head -1
```

## Documentation

- [Features Documentation](docs/FEATURES.md) - Comprehensive feature support reference
- [Deployment Guide](docs/DEPLOYMENT.md) - Detailed installation and configuration
- [kubectl Plugin](docs/KUBECTL-PLUGIN.md) - Command-line tool for volume management
- [Quick Start - NFS](docs/QUICKSTART.md) - Get started with NFS volumes
- [Quick Start - NVMe-oF](docs/QUICKSTART-NVMEOF.md) - Get started with NVMe-oF volumes
- [Quick Start - iSCSI](docs/QUICKSTART-ISCSI.md) - Get started with iSCSI volumes
- [Snapshots Guide](docs/SNAPSHOTS.md) - Volume snapshots and cloning
- [Versioning](docs/VERSIONING.md) - Version management and checking installed version
- [Distro Compatibility](docs/DISTRO-COMPATIBILITY.md) - Kubernetes distribution compatibility testing
- [Metrics Guide](docs/METRICS.md) - Prometheus metrics and monitoring
- [Kind Setup](docs/KIND.md) - Local development with Kind
- [Comparison with truenas-csi](docs/COMPARISON-TRUENAS-CSI.md) - vs official TrueNAS CSI driver
- [Comparison with Democratic-CSI](docs/COMPARISON-DEMOCRATIC-CSI.md) - vs democratic-csi

## Volume Adoption

The driver supports **cross-cluster volume adoption** - importing existing tns-csi managed volumes into a new Kubernetes cluster. This is useful for:
- Disaster recovery scenarios
- Cluster migrations
- Re-importing retained volumes after upgrades

Volumes are adoptable if they have proper `tns-csi:*` ZFS user properties set. See [Volume Adoption](docs/FEATURES.md#volume-adoption-cross-cluster) in the Features documentation for details.

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

# Run Ginkgo E2E tests (requires TrueNAS and Kubernetes cluster)
ginkgo -v --timeout=25m ./tests/e2e/nfs/...
ginkgo -v --timeout=40m ./tests/e2e/nvmeof/...
ginkgo -v --timeout=40m ./tests/e2e/iscsi/...
```

See [docs/TESTING.md](docs/TESTING.md) for comprehensive testing documentation.

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
