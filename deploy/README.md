# Deployment

## Standard Installation (Helm)

Helm is the recommended way to install tns-csi. The raw Kubernetes manifests that were previously in this directory have been removed in favor of the Helm chart.

### Quick Start

```bash
# Add the OCI registry (Docker Hub)
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

Or using GitHub Container Registry:

```bash
helm install tns-csi oci://ghcr.io/fenio/charts/tns-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"
```

### Version Pinning

**Always use a specific version in production.** The `--version` flag ensures you get a known, tested release.

To see available versions:
```bash
# Docker Hub
helm search repo oci://registry-1.docker.io/bfenski/tns-csi-driver --versions

# Or check GitHub releases
# https://github.com/fenio/tns-csi/releases
```

### Configuration

See the [Helm chart documentation](../charts/tns-csi-driver/README.md) for full configuration options.

Common configuration:

```bash
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set truenas.skipTLSVerify=true \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP" \
  --set storageClasses.nfs.pool="tank" \
  --set storageClasses.nvmeof.enabled=true \
  --set storageClasses.nvmeof.server="YOUR-TRUENAS-IP"
```

### Upgrading

```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.8.0 \
  --namespace kube-system \
  --reuse-values
```

### Uninstalling

```bash
helm uninstall tns-csi --namespace kube-system
```

## Why Helm?

The Helm chart provides:

1. **Version management** - Pin specific versions for reproducible deployments
2. **Configuration validation** - Fails fast on missing required values
3. **Sensible defaults** - Works out of the box with minimal configuration
4. **Easy upgrades** - `helm upgrade` handles rolling updates
5. **Templating** - Consistent naming and labeling across all resources
