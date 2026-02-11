# Versioning

This document describes the versioning strategy for tns-csi.

## Version Format

tns-csi follows [Semantic Versioning](https://semver.org/) (SemVer):

- **MAJOR.MINOR.PATCH** (e.g., `v0.9.2`)
- Tags are prefixed with `v` (e.g., `v0.9.2`, `v1.0.0`)

## Version Sources

The version is determined at **build time** and embedded in the binary. The version comes from:

1. **Git tags** (preferred) - When building from a tagged commit, the version is the tag name
2. **Git describe** - For non-tagged commits, format is `v0.9.2-3-gabc1234` (3 commits after v0.9.2)
3. **"dev"** - Fallback when git is not available

## What's Embedded

Each build includes:

| Field | Description | Example |
|-------|-------------|---------|
| Version | Semantic version from git tag | `v0.9.2` |
| Git Commit | Short SHA of the commit | `abc1234` |
| Build Date | UTC timestamp of build | `2025-12-21T10:30:00Z` |
| Go Version | Go compiler version | `go1.26.0` |
| Platform | OS and architecture | `linux/amd64` |

## Checking the Version

### From the Binary

```bash
tns-csi-driver --show-version
```

Output:
```
tns.csi.io version: v0.9.2
  Git commit: abc1234
  Build date: 2025-12-21T10:30:00Z
  Go version: go1.26.0
  Platform:   linux/amd64
```

### From the Metrics Endpoint

The driver exposes version info via HTTP:

```bash
# Port-forward to the controller pod
kubectl port-forward -n kube-system deployment/tns-csi-controller 8080:8080

# Query version endpoint
curl http://localhost:8080/version
```

Response:
```json
{
  "version": "v0.9.2",
  "gitCommit": "abc1234",
  "buildDate": "2025-12-21T10:30:00Z",
  "goVersion": "go1.26.0",
  "platform": "linux/amd64"
}
```

### From Container Logs

The version is logged at startup:
```
Starting TNS CSI Driver v0.9.2 (commit: abc1234, built: 2025-12-21T10:30:00Z)
```

### From Helm

Check which version is deployed:
```bash
helm list -n kube-system
```

## Docker Image Tags

### Release Tags

When a version is released, Docker images are tagged with:

| Tag | Description | Stability |
|-----|-------------|-----------|
| `v0.9.2` | Exact version | Immutable |
| `0.5` | Major.Minor | Points to latest patch |
| `0` | Major only | Points to latest minor |
| `latest` | Most recent release | Mutable - not recommended for production |

### Branch Tags

CI builds from branches are tagged with the branch name:
- `main` - Latest from main branch
- `feature-xyz` - Feature branch builds

## Helm Chart Versioning

The Helm chart version is kept in sync with the application version:

| Chart.yaml Field | Value |
|------------------|-------|
| `version` | `0.9.2` (chart version, no `v` prefix) |
| `appVersion` | `v0.9.2` (app version, with `v` prefix) |

### Image Tag Resolution

The Helm chart resolves the image tag in this order:

1. **Explicit override**: `--set image.tag=v0.9.2`
2. **Chart's appVersion**: Automatically uses `v0.9.2` when installing `--version 0.9.2`

## Best Practices

### For Production

**Always pin a specific version:**

```bash
# Install specific chart version (uses matching image tag automatically)
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.2 \
  ...
```

Or explicitly set the image tag:
```bash
helm install tns-csi ./charts/tns-csi-driver \
  --set image.tag=v0.9.2 \
  ...
```

### For Development

The `latest` tag and `main` branch builds are fine for development and testing:

```bash
helm install tns-csi ./charts/tns-csi-driver \
  --set image.tag=latest \
  --set image.pullPolicy=Always \
  ...
```

### Upgrading

Check current version before upgrading:
```bash
helm list -n kube-system
kubectl logs -n kube-system deployment/tns-csi-controller | head -1
```

Upgrade to a new version:
```bash
helm upgrade tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.9.2 \
  --reuse-values
```

## Reporting Issues

When reporting issues, always include the full version information:

```bash
# Get version from logs
kubectl logs -n kube-system deployment/tns-csi-controller 2>&1 | head -5

# Or from the API
kubectl exec -n kube-system deployment/tns-csi-controller -- \
  /usr/local/bin/tns-csi-driver --show-version
```

Include in your issue:
- Version (e.g., `v0.9.2`)
- Git commit (e.g., `abc1234`)
- How you installed (Helm version, custom image, etc.)
