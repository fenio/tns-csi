# TNS-CSI vs truenas-csi (Official TrueNAS CSI Driver)

The [official TrueNAS CSI driver](https://github.com/truenas/truenas-csi) was released by iXsystems in December 2025.

**Last Updated**: January 2026

## Overview

| Aspect | TNS-CSI | truenas-csi (Official) |
|--------|---------|------------------------|
| **Maintainer** | Community (fenio) | iXsystems |
| **License** | GPL-3.0 | GPL-3.0 |
| **TrueNAS Version** | Scale 25.10+ | Scale 25.10+ |
| **API Communication** | WebSocket API | WebSocket API |
| **Language** | Go | Go |

## Protocol Support

| Protocol | TNS-CSI | truenas-csi |
|----------|---------|-------------|
| **NFS** | Yes | Yes |
| **iSCSI** | No (intentionally excluded) | Yes |
| **NVMe-oF (TCP)** | Yes | No |

**Philosophy difference**: TNS-CSI deliberately excludes iSCSI in favor of NVMe-oF, which offers better performance on modern networks (10GbE+). The official driver takes the opposite approach - supporting the mature iSCSI protocol but not NVMe-oF.

## Feature Comparison

| Feature | TNS-CSI | truenas-csi |
|---------|---------|-------------|
| **Dynamic Provisioning** | Yes | Yes |
| **Volume Expansion** | Yes | Yes |
| **Snapshots** | Yes | Yes |
| **Volume Cloning** | Yes | Yes |
| **ZFS Compression** | Yes | Yes |
| **ZFS Sync Modes** | Yes | Yes |
| **Detached Snapshots** | Yes | No |
| **Dataset Encryption** | No | Yes |
| **Automatic Snapshot Scheduling** | No | Yes |
| **CHAP Authentication** | N/A (no iSCSI) | Yes |
| **kubectl Plugin** | Yes | No |
| **Volume Adoption/Migration** | Yes | No |
| **Prometheus Metrics** | Yes | No |
| **Orphan Volume Detection** | Yes | No |

## Unique to TNS-CSI

### 1. kubectl Plugin (`kubectl tns-csi`)

A comprehensive command-line tool for volume management:

- `kubectl tns-csi summary` - Dashboard overview of all resources
- `kubectl tns-csi list` - List all managed volumes
- `kubectl tns-csi list-orphaned` - Find volumes with no matching PVC
- `kubectl tns-csi list-unmanaged` - Discover datasets not managed by tns-csi
- `kubectl tns-csi import` - Import existing datasets into management
- `kubectl tns-csi adopt` - Generate manifests for volume adoption
- `kubectl tns-csi health` - Check health of all volumes
- `kubectl tns-csi troubleshoot` - Diagnose PVC issues

### 2. Detached Snapshots

- Uses `zfs send/receive` to create independent dataset copies
- Survives deletion of source volume
- Useful for backup/DR scenarios
- Can be restored even after original volume is deleted

### 3. Volume Adoption/Migration

- Mark volumes as "adoptable" for cluster migration
- Import existing datasets into tns-csi management
- Re-adopt volumes after cluster rebuild
- Migration assistance from democratic-csi

### 4. Prometheus Metrics

Built-in observability:
- Volume operation latencies (create, delete, expand)
- Error rates by operation type
- Volume capacity tracking
- Request counts and durations

### 5. NVMe-oF Support

- Modern block storage protocol
- Better performance than iSCSI on fast networks (10GbE+)
- Lower CPU overhead
- Native multipath support

## Unique to truenas-csi (Official)

### 1. Dataset Encryption

- AES-256-GCM and AES-128-CCM algorithms
- Passphrase or hex key support
- Automatic key generation capability
- Encryption managed at ZFS dataset level

### 2. Automatic Snapshot Scheduling

- Cron-based scheduling directly in StorageClass
- Configurable retention policies (hourly to yearly)
- Custom naming schemas with timestamps
- No external snapshot controller needed for scheduled snapshots

### 3. iSCSI with CHAP

- Mature, widely-compatible block storage protocol
- CHAP authentication (including mutual)
- Initiator IQN filtering
- Network CIDR restrictions
- Broad client compatibility

### 4. Official Support

- Maintained by iXsystems (TrueNAS developers)
- Likely to have better long-term support
- Integration with TrueNAS roadmap
- Official documentation and support channels

## When to Choose Each

### Choose TNS-CSI if:

- You want **NVMe-oF** for high-performance block storage
- You need **volume adoption/migration** features
- You want a **kubectl plugin** for volume management
- You're migrating from **democratic-csi** and want similar workflows
- You need **Prometheus metrics** for monitoring
- You want **detached snapshots** for backup/DR

### Choose truenas-csi (Official) if:

- You need **iSCSI** (established infrastructure, compatibility requirements)
- You want **dataset encryption** at the ZFS level
- You need **automatic snapshot scheduling** without external tools
- You prefer **official vendor support**
- You want the safety of an **iXsystems-maintained** project

## Maturity

| Aspect | TNS-CSI | truenas-csi |
|--------|---------|-------------|
| **Project Age** | ~6 months | ~1 month (Dec 2025) |
| **Production Use** | Homelab tested | Unknown |
| **Test Coverage** | Unit + E2E tests | Unknown |
| **Documentation** | Comprehensive | Good |

**Note**: The official truenas-csi is very new (created December 2025). While it has iXsystems backing, it may still have early-stage issues. TNS-CSI has been in development longer but lacks official vendor support.

## Migration Between Drivers

Both drivers store metadata in ZFS user properties, but with different property prefixes:
- TNS-CSI: `tns-csi:*` properties
- truenas-csi: Different property schema

Direct migration between the two would require re-importing volumes. TNS-CSI's `kubectl tns-csi import` command can help adopt datasets created by other tools.

## Related Links

- [TNS-CSI GitHub](https://github.com/fenio/tns-csi)
- [truenas-csi GitHub](https://github.com/truenas/truenas-csi) (Official)
