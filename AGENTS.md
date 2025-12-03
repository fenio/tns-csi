# Agent Instructions for CSI Driver Development

## Project Status

This CSI driver is in **early development phase** with the following features:
- ✅ NFS storage provisioning (functional, testing in progress)
- ✅ NVMe-oF (TCP) storage provisioning (functional, testing in progress)
- ✅ Volume expansion for both NFS and NVMe-oF protocols (functional)
- ✅ WebSocket client with connection resilience (stable)
- ✅ Self-hosted GitHub runner for integration testing
- ✅ Automated CI/CD with GitHub Actions

**This is NOT production-ready software.** It requires extensive testing and validation before use in production environments.

### Protocol Focus: NFS and NVMe-oF Only

**IMPORTANT:** This driver exclusively supports NFS and NVMe-oF protocols. Do not implement or suggest adding:
- **iSCSI support** - NVMe-oF is superior in performance (lower latency, higher IOPS) and is the preferred block storage protocol
- **SMB/CIFS support** - Low priority due to author preference for Linux-native protocols

Any work on additional protocols should be explicitly requested by the project maintainer. Focus development efforts on improving the existing NFS and NVMe-oF implementations.

## Critical Development Practices

### 1. **Testing Infrastructure**

This project uses **self-hosted infrastructure** for all testing:
- **Self-hosted GitHub runner** - TrueNAS related CI/CD runs on dedicated hardware
- **Self-hosted TrueNAS server** - Real TrueNAS instance for integration tests
- **GitHub Actions workflows** - `.github/workflows/integration.yml` runs both NFS and NVMe-oF tests
- **Secrets management** - TrueNAS credentials stored in GitHub repository secrets

**ALWAYS:**
- Use GitHub Actions workflows for testing (never suggest manual test scripts)
- Verify changes with both NFS and NVMe-oF integration tests
- Check workflow runs at: https://github.com/fenio/tns-csi/actions
- Tests run on every push to main and on pull requests

**NEVER:**
- Suggest setting up local Kind clusters for testing (we use k3s on self-hosted runner)
- Recommend mock TrueNAS APIs (we test against real TrueNAS)
- Propose skipping integration tests (they're fast with self-hosted infrastructure)

### 2. **Core Systems - Handle with Care**

The following components are functional but in active development:

#### WebSocket Client (`pkg/tnsapi/client.go`)
- Working ping/pong loop with 30-second intervals
- Automatic reconnection with exponential backoff
- Proper read/write deadline management
- **Modify carefully** - test thoroughly with integration tests after changes

#### Storage Provisioning
- NFS provisioning in `controller_nfs.go` (createNFSVolume, deleteNFSVolume, expandNFSVolume)
- NVMe-oF provisioning in `controller_nvmeof.go` (createNVMeOFVolume, deleteNVMeOFVolume, expandNVMeOFVolume)
- Volume expansion implemented and functional for both protocols
- Node operations in `node.go` (stage, publish, unpublish)
- **Test thoroughly** - always run integration tests for both protocols after changes

### 3. **What to Focus On**

When working on this project, prioritize:
- **New features for NFS/NVMe-oF**: Snapshots, cloning, improved volume expansion
- **Error handling improvements**: Better error messages, retry logic
- **Observability**: Metrics, additional logging for troubleshooting
- **Documentation**: Usage guides, troubleshooting tips, performance tuning
- **Performance optimization**: Based on profiling data, not speculation
- **NFS/NVMe-oF enhancements**: Improved mount options, better multipathing, connection optimization

**Do NOT work on:**
- iSCSI protocol implementation (NVMe-oF is the preferred block storage protocol)
- SMB/CIFS protocol implementation (low priority, Linux-focused driver)

### 4. **Development Workflow**

Standard development cycle:
1. Make changes locally
2. **Run unit tests and linter locally before committing** (see Pre-Commit Checklist below)
3. Push to repository (triggers CI/CD automatically)
4. Monitor GitHub Actions workflow runs
5. Review integration test results (NFS and NVMe-oF)
6. Iterate based on test feedback

#### Pre-Commit Checklist

**ALWAYS run these commands before committing or pushing changes:**

```bash
# Run unit tests
go test ./pkg/... -count=1

# Run linter
golangci-lint run

# Build to verify compilation
go build ./...
```

**All three must pass before pushing.** Failing to run these locally wastes CI/CD resources and delays development.

**Do not suggest:**
- Local testing setups that bypass CI/CD
- Manual deployment scripts (use Helm charts)
- Skipping integration tests to save time
- Pushing without running local tests first

### 5. **Debugging Approach**

When investigating issues:

1. **Check GitHub Actions logs first** - Most issues appear in CI/CD runs
2. **Review both controller and node logs** - Issues often span both components
3. **Verify against both protocols** - Test with both NFS and NVMe-oF
4. **Check TrueNAS state** - Dataset creation, NFS shares, NVMe-oF targets
5. **Consider Kubernetes factors** - PVC binding, pod scheduling, volume attachment

**Do not immediately:**
- Modify core connection handling code
- Add excessive debug logging (use appropriate klog verbosity levels)
- Suggest architectural changes without evidence

### 6. **Code Quality Standards**

Before suggesting changes:
- **Is there evidence of a problem?** (logs, error reports, failed tests)
- **Will this affect working functionality?** (run integration tests)
- **Is this solving a real issue?** (not theoretical improvements)
- **Have you checked recent commits?** (avoid redoing recent work)
- **Does this align with CSI spec?** (maintain CSI compliance)

## TrueNAS API Reference

Quick reference for TrueNAS API usage:

**Connection:**
- WebSocket endpoint: `wss://<host>/api/current`
- Authentication: `auth.login_with_api_key`
- Format: JSON-RPC 2.0

**NFS APIs:**
- Create share: `sharing.nfs.create`
- Delete share: `sharing.nfs.delete`
- List shares: `sharing.nfs.query`

**NVMe-oF APIs:**
- Create target: `iscsi.target.create` (reused for NVMe-oF)
- List portals: `iscsi.portal.query`
- Target management: Standard iSCSI API subset

**Dataset APIs:**
- Create: `pool.dataset.create`
- Delete: `pool.dataset.delete`
- Query: `pool.dataset.query`

## Summary

This is a **functional CSI driver in early development**. Focus on:
- Testing and validating existing features (NFS, NVMe-oF, snapshots, expansion)
- Adding new capabilities (metrics, cloning improvements, health checks)
- Improving error handling and user experience
- Documenting edge cases and known issues
- Building comprehensive test coverage

Critical practices:
- Always run integration tests after changes
- Test both NFS and NVMe-oF protocols
- Document any discovered issues or limitations
- Follow the established CI/CD pipeline
- Be thorough - this is early development software
