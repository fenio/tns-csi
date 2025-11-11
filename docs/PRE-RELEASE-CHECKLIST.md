# Pre-Release Checklist

Use this checklist before creating a new release to ensure everything is ready.

## Version Planning

- [ ] **Determine version number** following [Semantic Versioning](https://semver.org/)
  - MAJOR: Breaking changes (v1.0.0 → v2.0.0)
  - MINOR: New features, backwards compatible (v1.0.0 → v1.1.0)
  - PATCH: Bug fixes, backwards compatible (v1.0.0 → v1.0.1)
- [ ] **Update CHANGELOG.md** with release notes
  - Move items from [Unreleased] to new version section
  - Include date: `## [X.Y.Z] - YYYY-MM-DD`
  - Categorize changes: Added, Changed, Deprecated, Removed, Fixed, Security
- [ ] **Review version consistency** across files:
  - [ ] `Makefile` - `VERSION?=vX.Y.Z`
  - [ ] `charts/tns-csi-driver/Chart.yaml` - `version: X.Y.Z` and `appVersion: "vX.Y.Z"`
  - [ ] `charts/tns-csi-driver/values.yaml` - `image.tag: "vX.Y.Z"`

## Code Quality

- [ ] **Run unit tests**: `make test`
  ```bash
  make test
  ```
- [ ] **Run linter**: `make lint`
  ```bash
  make lint
  ```
- [ ] **Fix any linter issues**: `make lint-fix`
  ```bash
  make lint-fix
  ```
- [ ] **Build successfully**: `make build`
  ```bash
  make build
  ```
- [ ] **Test binary version flag**: `./bin/tns-csi-driver --version`
  ```bash
  ./bin/tns-csi-driver --version
  ```

## Integration Testing

- [ ] **Verify GitHub Actions CI passing** on main branch
  - Check: https://github.com/fenio/tns-csi/actions
  - [ ] CI workflow (build, test, lint)
  - [ ] Integration tests (NFS)
  - [ ] Integration tests (NVMe-oF)
  - [ ] Sanity tests (CSI compliance)
- [ ] **Review test results dashboard**
  - Check: https://fenio.github.io/tns-csi/dashboard/
  - Ensure recent test runs are green

## Documentation

- [ ] **Update README.md** if needed
  - [ ] Version badges reflect latest
  - [ ] Installation instructions are current
  - [ ] Feature list is accurate
  - [ ] Prerequisites are up to date
- [ ] **Review and update documentation** in `docs/`:
  - [ ] `DEPLOYMENT.md` - deployment instructions current
  - [ ] `QUICKSTART.md` - NFS quickstart accurate
  - [ ] `QUICKSTART-NVMEOF.md` - NVMe-oF quickstart accurate
  - [ ] `FEATURES.md` - feature matrix complete
  - [ ] `RELEASE.md` - release process documented
- [ ] **Check for outdated documentation**
  - [ ] No references to old versions
  - [ ] No broken links
  - [ ] Examples use correct syntax

## Security Review

- [ ] **Review SECURITY.md** for accuracy
- [ ] **Check for exposed secrets** in code
  ```bash
  git grep -i "api.key\|password\|secret" | grep -v ".md"
  ```
- [ ] **Verify .gitignore** includes sensitive files
  - [ ] `*.local.yaml`
  - [ ] `.env`
  - [ ] `secrets/`
- [ ] **Review dependencies** for known vulnerabilities
  ```bash
  go list -m all | grep -v "indirect"
  ```

## Container Images

- [ ] **Verify Dockerfile** is up to date
  - [ ] Base image versions current
  - [ ] All required dependencies included
  - [ ] Multi-stage build optimized
- [ ] **Test local build**
  ```bash
  make docker-build
  docker run --rm bfenski/tns-csi:latest --version
  ```
- [ ] **Verify GitHub secrets** are configured
  - [ ] `DOCKERHUB_USERNAME` set
  - [ ] `DOCKERHUB_TOKEN` set and valid
  - Check: Repository Settings → Secrets and variables → Actions

## Helm Chart

- [ ] **Validate Helm chart**
  ```bash
  helm lint charts/tns-csi-driver
  ```
- [ ] **Test Helm chart rendering**
  ```bash
  helm template tns-csi charts/tns-csi-driver \
    --set truenas.url="wss://test.local/api/current" \
    --set truenas.apiKey="test-key" \
    --set storageClasses.nfs.enabled=true \
    --set storageClasses.nfs.pool="test-pool" \
    --set storageClasses.nfs.server="test.local"
  ```
- [ ] **Review chart README**
  ```bash
  cat charts/tns-csi-driver/README.md
  ```
- [ ] **Verify values.yaml** has sensible defaults

## Release Workflow

- [ ] **Review release workflow** (`.github/workflows/release.yml`)
  - [ ] Workflow triggers on version tags
  - [ ] Docker Hub push enabled
  - [ ] GHCR push enabled
  - [ ] Helm chart packaging included
  - [ ] GitHub release creation configured
- [ ] **Ensure main branch is clean**
  ```bash
  git status
  git pull origin main
  ```
- [ ] **No uncommitted changes**
- [ ] **All PRs merged** that should be in release

## Communication

- [ ] **Draft release announcement** (optional for early releases)
- [ ] **Notify users** of breaking changes (if any)
- [ ] **Update project status** if moving to stable

## Final Checks

- [ ] **All checklist items completed** ✅
- [ ] **Version number finalized**: `vX.Y.Z`
- [ ] **Ready to tag and release**

## Release Commands

Once all checks pass, create and push the release tag:

```bash
# Ensure you're on main branch
git checkout main
git pull origin main

# Create version tag
VERSION=vX.Y.Z  # Replace with actual version
git tag -a $VERSION -m "Release $VERSION"

# Push tag to trigger release workflow
git push origin $VERSION

# Monitor release workflow
# Visit: https://github.com/fenio/tns-csi/actions
```

## Post-Release

After the release workflow completes:

- [ ] **Verify Docker images published**
  - Docker Hub: https://hub.docker.com/r/bfenski/tns-csi/tags
  - GHCR: https://github.com/fenio?tab=packages
- [ ] **Verify Helm chart published**
  ```bash
  helm show chart oci://registry-1.docker.io/bfenski/tns-csi-driver --version X.Y.Z
  ```
- [ ] **Verify GitHub release created**
  - Check: https://github.com/fenio/tns-csi/releases/latest
  - [ ] Release notes accurate
  - [ ] Helm chart tarball attached
- [ ] **Test installation from published artifacts**
  ```bash
  helm install tns-csi-test oci://registry-1.docker.io/bfenski/tns-csi-driver \
    --version X.Y.Z \
    --namespace test \
    --create-namespace \
    --set truenas.url="wss://test.local/api/current" \
    --set truenas.apiKey="test-key" \
    --dry-run
  ```
- [ ] **Update CHANGELOG.md** with link to release
- [ ] **Create [Unreleased] section** in CHANGELOG.md for next version

## Rollback Procedure

If the release has critical issues:

```bash
# Delete the tag locally
git tag -d vX.Y.Z

# Delete the tag remotely
git push --delete origin vX.Y.Z

# Delete Docker images (if needed)
# This requires manual deletion from Docker Hub and GHCR web interfaces

# Delete GitHub release (if needed)
gh release delete vX.Y.Z
```

## Notes

- **Early Development Phase**: During v0.x.x, breaking changes are expected and acceptable
- **Production Readiness**: Major version v1.0.0 should only be released when the driver is production-ready
- **Communication**: For v1.0.0 and later, provide migration guides for breaking changes
