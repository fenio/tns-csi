# Release Process

This document describes how to create a new release of the TNS CSI Driver.

## Prerequisites

Before creating your first release, you need to configure GitHub secrets for Docker Hub authentication.

### Required GitHub Secrets

Navigate to your repository settings: `Settings` → `Secrets and variables` → `Actions` → `New repository secret`

Add the following secrets:

1. **DOCKERHUB_USERNAME**
   - Your Docker Hub username
   - Example: `bfenski`

2. **DOCKERHUB_TOKEN**
   - Docker Hub access token (NOT your password)
   - Generate at: https://hub.docker.com/settings/security
   - Click "New Access Token"
   - Give it a descriptive name like "GitHub Actions - TNS CSI"
   - Select "Read, Write, Delete" permissions
   - Copy the token (you won't see it again)

### Verify Secrets

After adding secrets, they should appear in:
- Repository Settings → Secrets and variables → Actions → Repository secrets

## Release Workflow

The release process is fully automated via GitHub Actions.

### Creating a Release

1. **Ensure main branch is ready**
   ```bash
   git checkout main
   git pull origin main
   ```

2. **Verify tests pass**
   ```bash
   make test
   make lint
   ```

3. **Create and push a version tag**
   ```bash
   # Use semantic versioning (v1.0.0, v1.2.3, etc.)
   git tag v1.0.0
   git push origin v1.0.0
   ```

4. **Monitor the release**
   - Go to: https://github.com/bfenski/tns-csi/actions
   - Watch the "Release" workflow run
   - The workflow will:
     - Run tests and linters
     - Build multi-arch Docker images (amd64, arm64)
     - Push images to Docker Hub and GitHub Container Registry
     - Package Helm chart
     - Publish Helm chart to Docker Hub and GHCR as OCI artifacts
     - Create GitHub release with changelog
     - Attach Helm chart tarball to release

5. **Verify release artifacts**
   - Docker Hub: https://hub.docker.com/r/bfenski/tns-csi
   - GitHub Releases: https://github.com/bfenski/tns-csi/releases
   - GHCR: https://github.com/bfenski?tab=packages

## What Gets Published

Each release creates the following artifacts:

### Docker Images
- **Docker Hub**: `bfenski/tns-csi:v1.0.0`, `bfenski/tns-csi:1.0`, `bfenski/tns-csi:1`, `bfenski/tns-csi:latest`
- **GHCR**: `ghcr.io/bfenski/tns-csi:v1.0.0`, etc.
- **Architectures**: linux/amd64, linux/arm64

### Helm Charts
- **Docker Hub OCI**: `oci://registry-1.docker.io/bfenski/tns-csi-driver`
- **GHCR OCI**: `oci://ghcr.io/bfenski/charts/tns-csi-driver`
- **GitHub Release**: `tns-csi-driver-1.0.0.tgz` attached to release

### GitHub Release
- Automatic changelog from git commits
- Installation instructions
- Links to Docker images and Helm charts
- Attached Helm chart tarball

## Version Tagging Strategy

We follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version (v1.0.0 → v2.0.0): Breaking changes
- **MINOR** version (v1.0.0 → v1.1.0): New features, backwards compatible
- **PATCH** version (v1.0.0 → v1.0.1): Bug fixes, backwards compatible

### Examples

```bash
# First release
git tag v1.0.0
git push origin v1.0.0

# Bug fix release
git tag v1.0.1
git push origin v1.0.1

# New feature release
git tag v1.1.0
git push origin v1.1.0

# Breaking change release
git tag v2.0.0
git push origin v2.0.0
```

## Development Builds

The CI workflow automatically builds and pushes development images on every push to `main`:

- **Docker Hub**: `bfenski/tns-csi:latest`
- **GHCR**: `ghcr.io/bfenski/tns-csi:latest`

These are useful for testing but should **not** be used in production.

## Helm Chart Versioning

The release workflow automatically updates:
- `charts/tns-csi-driver/Chart.yaml` - `version` and `appVersion` fields
- `charts/tns-csi-driver/values.yaml` - `image.tag` field

These changes are included in the packaged chart but not committed back to the repository.

## Testing a Release

After publishing a release, test it:

```bash
# Test Docker image
docker pull bfenski/tns-csi:v1.0.0
docker run --rm bfenski/tns-csi:v1.0.0 --version

# Test Helm chart
helm install tns-csi-test oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 1.0.0 \
  --namespace test \
  --create-namespace \
  --set truenas.url="wss://truenas.local/api/current" \
  --set truenas.apiKey="test-key" \
  --dry-run
```

## Troubleshooting

### Release workflow fails on Docker push

**Error**: `denied: requested access to the resource is denied`

**Solution**: 
1. Verify `DOCKERHUB_USERNAME` secret matches your Docker Hub username exactly
2. Verify `DOCKERHUB_TOKEN` is a valid access token (not password)
3. Regenerate token if needed: https://hub.docker.com/settings/security

### Release workflow fails on Helm push

**Error**: `unauthorized: authentication required`

**Solution**: This shouldn't happen as the workflow uses the same DOCKERHUB_TOKEN for Helm chart publishing. Verify the token has "Read, Write, Delete" permissions.

### Tag already exists

**Error**: `tag 'v1.0.0' already exists`

**Solution**:
```bash
# Delete local tag
git tag -d v1.0.0

# Delete remote tag
git push --delete origin v1.0.0

# Recreate tag at current commit
git tag v1.0.0
git push origin v1.0.0
```

### Multi-arch build fails

**Error**: Platform build failures

**Solution**: The workflow uses GitHub-hosted runners which support multi-arch builds via QEMU. If builds are slow or fail, consider:
1. Using self-hosted runners with native arm64 support
2. Removing arm64 from platforms (line 69 in `.github/workflows/release.yml`)

## Manual Release (Emergency)

If GitHub Actions is unavailable, you can release manually:

```bash
# 1. Set version
VERSION=v1.0.0

# 2. Build and push Docker image
docker buildx build --platform linux/amd64,linux/arm64 \
  -t bfenski/tns-csi:${VERSION} \
  -t bfenski/tns-csi:latest \
  --push .

# 3. Update Helm chart versions
sed -i "s/^version:.*/version: ${VERSION#v}/" charts/tns-csi-driver/Chart.yaml
sed -i "s/^appVersion:.*/appVersion: \"${VERSION}\"/" charts/tns-csi-driver/Chart.yaml

# 4. Package and push Helm chart
helm package charts/tns-csi-driver
echo $DOCKERHUB_TOKEN | helm registry login registry-1.docker.io -u $DOCKERHUB_USERNAME --password-stdin
helm push tns-csi-driver-${VERSION#v}.tgz oci://registry-1.docker.io/bfenski

# 5. Create GitHub release manually via web UI
# https://github.com/bfenski/tns-csi/releases/new
```

## References

- [Semantic Versioning](https://semver.org/)
- [Docker Hub OCI Support](https://docs.docker.com/docker-hub/oci-artifacts/)
- [Helm OCI Registries](https://helm.sh/docs/topics/registries/)
- [GitHub Actions Workflows](.github/workflows/)
