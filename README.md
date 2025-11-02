# TNS CSI Driver

A Kubernetes CSI (Container Storage Interface) driver for TrueNAS and systems with TNS-compatible APIs.

## Important Disclaimer

**This is an independent, community-developed project and is NOT affiliated with, endorsed by, or supported by iXsystems Inc. or the TrueNAS project.**

This driver is designed to work with TrueNAS and systems that provide TrueNAS-compatible APIs, but:
- It is not an official TrueNAS product
- It is not supported by iXsystems Inc.
- TrueNAS is a registered trademark of iXsystems Inc.
- Use of this software is entirely at your own risk

If you need official support, please use the official TrueNAS CSI driver available at https://github.com/truenas/charts

## Overview

This CSI driver enables Kubernetes to provision and manage persistent volumes on TrueNAS and systems with TNS-compatible APIs. It supports multiple storage protocols:

- **NFS** - Network File System for file-based storage
- **NVMe-oF** - NVMe over Fabrics for high-performance block storage
- **iSCSI** - (Planned) Internet Small Computer Systems Interface

## Features

- Dynamic volume provisioning
- Multiple protocol support (NFS, NVMe-oF)
- Volume lifecycle management
- Support for ReadWriteOnce and ReadWriteMany access modes
- Integration with Kubernetes storage classes

## Prerequisites

- Kubernetes 1.20+
- TrueNAS or compatible system with TNS-compatible API (v2.0 WebSocket API)
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu, `nfs-utils` on RHEL/CentOS)
- For NVMe-oF: 
  - `nvme-cli` package installed on all nodes
  - Kernel modules: `nvme-tcp`, `nvme-fabrics`
  - Network connectivity to TrueNAS on port 4420

## Quick Start

See [DEPLOYMENT.md](DEPLOYMENT.md) for detailed installation and configuration instructions.

### Installation via Helm (Recommended)

The easiest way to install the TNS CSI Driver is using Helm. The chart is available on Docker Hub as an OCI artifact:

```bash
# Install from OCI registry (Docker Hub)
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:1443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

Or install from local chart:
```bash
helm install tns-csi ./charts/tns-csi-driver -n kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:1443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

**Example NFS-only deployment:**
```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:1443/api/current" \
  --set truenas.apiKey="your-api-key-here" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

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
  path: /mnt/tank/k8s/nvmeof
  fsType: ext4  # or xfs
```

## Known Limitations

- **Volume Deletion**: Implemented for NFS and NVMe-oF. Datasets, shares, subsystems, and namespaces are cleaned up on PVC deletion. (iSCSI deletion not yet implemented).
- **Protocol Support**: NFS and NVMe-oF are implemented. iSCSI is planned for future releases.
- **Volume Expansion**: Supported via Kubernetes when `allowVolumeExpansion: true` is set in the StorageClass (Helm chart enables this by default for NFS)
- **Snapshots**: Not yet implemented
- **Testing**: Limited testing on production environments - use with caution

## Troubleshooting

See [DEPLOYMENT.md](DEPLOYMENT.md#troubleshooting) for detailed troubleshooting steps.

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

## Development

### Building

```bash
make build
```

### Testing

See [TESTING.md](TESTING.md) for testing procedures.

### Building Container Image

```bash
make docker-build
```

## Contributing

Contributions are welcome! Please feel free to submit issues and pull requests.

## License

This project is licensed under the GNU General Public License v3.0 (GPL-3.0) - see the LICENSE file for details.

## Acknowledgments

This driver is designed to work with TrueNAS and systems that provide TrueNAS-compatible APIs. TrueNAS is a trademark of iXsystems Inc.
