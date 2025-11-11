# Changelog

All notable changes to the TNS CSI Driver project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial implementation of CSI driver for TrueNAS Scale 25.10+
- NFS protocol support for file-based storage
- NVMe-oF (TCP) protocol support for high-performance block storage
- Dynamic volume provisioning and deletion
- Volume lifecycle management (create, delete, attach, detach, mount, unmount)
- Volume snapshots (create, delete, restore)
- Volume cloning from snapshots
- Volume expansion for both NFS and NVMe-oF protocols
- WebSocket client with connection resilience and automatic reconnection
- Prometheus metrics endpoint for observability
- Comprehensive integration test suite on real infrastructure
- Helm chart for easy deployment
- Multi-architecture support (amd64, arm64)
- Automated CI/CD with GitHub Actions
- Self-hosted runner infrastructure for testing
- Comprehensive documentation (deployment, quickstart, features, testing)

### Changed
- N/A (initial release)

### Deprecated
- N/A (initial release)

### Removed
- N/A (initial release)

### Fixed
- N/A (initial release)

### Security
- WebSocket connections use WSS (secure WebSocket) by default
- API keys stored in Kubernetes Secrets
- TLS certificate validation for production deployments

## Release Notes

This is an **early development release** and is **NOT production-ready**. Use at your own risk in development and testing environments only.

### Known Limitations
- Limited to NFS and NVMe-oF protocols (iSCSI and SMB not supported)
- Requires TrueNAS Scale 25.10 or later
- NVMe-oF requires pre-configured subsystem on TrueNAS
- Volume expansion requires `allowVolumeExpansion: true` in StorageClass
- Self-signed TLS certificates may require additional configuration

### Testing Status
- ✅ CSI sanity tests passing
- ✅ NFS integration tests passing
- ✅ NVMe-oF integration tests passing
- ✅ Snapshot and clone tests passing
- ✅ Volume expansion tests passing
- ✅ Connection resilience tests passing
- ✅ Concurrent volume creation tests passing

### Compatibility
- **Kubernetes**: 1.27+ (earlier versions may work but are untested)
- **TrueNAS Scale**: 25.10+ required
- **Go**: 1.21+ for development
- **Architectures**: linux/amd64, linux/arm64

---

## Version History Template

Use this template for future releases:

```markdown
## [X.Y.Z] - YYYY-MM-DD

### Added
- New features

### Changed
- Changes to existing functionality

### Deprecated
- Features that will be removed in future releases

### Removed
- Features removed in this release

### Fixed
- Bug fixes

### Security
- Security-related changes
```
