# TNS-CSI vs Democratic-CSI: Comparison

This document compares TNS-CSI with [democratic-csi](https://github.com/democratic-csi/democratic-csi), the most popular community CSI driver for TrueNAS.

**Last Updated**: December 2025

## Overview

| Aspect | TNS-CSI | Democratic-CSI |
|--------|---------|----------------|
| **Maturity** | Early development, not production-ready | Mature, established (1.2k+ stars) |
| **Language** | Go | JavaScript (Node.js) |
| **License** | GPL v3 | MIT |
| **TrueNAS Version** | TrueNAS Scale 25.10+ only | FreeNAS/TrueNAS (multiple versions) |
| **API Connection** | WebSocket API only (no SSH) | SSH-based or HTTP API (experimental) |

## Protocol Support

| Protocol | TNS-CSI | Democratic-CSI |
|----------|---------|----------------|
| **NFS** | Yes | Yes |
| **NVMe-oF** | Yes (primary block protocol) | Yes (zfs-generic-nvmeof driver) |
| **iSCSI** | No (by design) | Yes (primary block protocol) |
| **SMB/CIFS** | No (low priority) | Yes |

## Key Differences

### Architecture Philosophy

**TNS-CSI:**
- Focused on modern protocols (NFS + NVMe-oF)
- WebSocket-based API communication (no SSH required)
- Single-purpose: TrueNAS Scale 25.10+ only
- Deliberately avoids iSCSI in favor of NVMe-oF for better performance
- Native Go implementation with minimal dependencies

**Democratic-CSI:**
- Multi-backend support (TrueNAS, ZoL, Synology, ObjectiveFS, etc.)
- Primarily SSH-based with experimental API-only drivers (`freenas-api-*`)
- Broader compatibility with older TrueNAS/FreeNAS versions
- iSCSI as the primary block storage protocol
- Node.js implementation with extensive driver ecosystem

### Backend Support

**TNS-CSI:**
- TrueNAS Scale 25.10+ (exclusively)

**Democratic-CSI:**
- FreeNAS / TrueNAS (CORE and SCALE)
- ZFS on Linux (Ubuntu, etc.)
- Synology (experimental)
- ObjectiveFS
- Lustre (client mode)
- Local hostpath provisioning
- NFS/SMB client modes
- Node-local ZFS (dataset/zvol)

### Features

| Feature | TNS-CSI | Democratic-CSI |
|---------|---------|----------------|
| Dynamic provisioning | Yes | Yes |
| Volume expansion | Yes | Yes |
| Snapshots | Yes | Yes |
| Cloning | Yes | Yes |
| Detached snapshots | Yes | No |
| RWX (ReadWriteMany) | Yes (NFS) | Yes |
| Volume health monitoring | Yes (GET_VOLUME) | No |
| Volume name templating | Yes | Yes |
| Delete strategy (retention) | Yes | No |
| Configurable mount options | Yes | Yes |
| ZFS property configuration | Yes | Limited |
| Windows nodes | No | Yes (v1.7.0+) |
| Multipath | NVMe-native | iSCSI multipath |
| Local ephemeral volumes | No | Yes |
| Prometheus metrics | Yes | No (basic) |

### Configuration Complexity

**TNS-CSI:**
- Simpler configuration (fewer options)
- Requires pre-configured NVMe-oF subsystem in TrueNAS
- Helm chart or kubectl manifests
- No SSH setup required
- API key authentication only

**Democratic-CSI:**
- More complex configuration with many options
- Requires SSH setup and potentially sudo configuration for most drivers
- Experimental `freenas-api-*` drivers work without SSH (SCALE 21.08+)
- Helm chart with extensive example values
- May require shell configuration on TrueNAS

## When to Choose Each

### Choose TNS-CSI if:

- You're running TrueNAS Scale 25.10+
- You want NVMe-oF for block storage (better performance than iSCSI)
- You prefer a simpler, focused driver with fewer moving parts
- You don't want to configure SSH access to your NAS
- You need volume health monitoring (ControllerGetVolume)
- You want comprehensive Prometheus metrics
- You're comfortable with early-stage software
- You prefer native Go implementation

### Choose Democratic-CSI if:

- You need production-ready, battle-tested software
- You're running older TrueNAS/FreeNAS versions or TrueNAS CORE
- You need iSCSI or SMB support
- You need Windows node support
- You want multi-backend flexibility (ZoL, Synology, ObjectiveFS, etc.)
- You need local/ephemeral volume support
- You need Nomad or Docker Swarm support

## Why NVMe-oF Over iSCSI?

TNS-CSI deliberately chose NVMe-oF as its block storage protocol instead of iSCSI:

- **Lower latency**: NVMe-oF has significantly lower protocol overhead
- **Higher IOPS**: Designed for modern NVMe SSDs and their parallel I/O capabilities
- **Simpler stack**: No SCSI translation layer
- **Future-proof**: NVMe-oF is the direction the industry is moving

For workloads that can benefit from high-performance block storage, NVMe-oF provides measurable improvements over iSCSI.

## Summary

| | TNS-CSI | Democratic-CSI |
|---|---------|----------------|
| **Best for** | Modern TrueNAS Scale with NVMe-oF | Broad compatibility, production use |
| **Trade-off** | Newer, less tested | More complex setup |
| **Block protocol** | NVMe-oF (higher performance) | iSCSI (wider compatibility) |
| **Unique features** | Volume health monitoring, metrics, detached snapshots | Multi-backend, Windows support |

**Democratic-CSI** is the mature, feature-rich choice with broad compatibility and a large community. It's production-ready and supports many backends and protocols. It also has NVMe-oF support via `zfs-generic-nvmeof` for ZFS-on-Linux setups.

**TNS-CSI** is a newer, purpose-built driver for modern TrueNAS Scale deployments that prioritizes NVMe-oF over iSCSI for superior block storage performance. It offers unique features like CSI volume health monitoring (GET_VOLUME capability), comprehensive Prometheus metrics, and detached snapshots (independent clones that can outlive their source snapshot). It's simpler but still in early development and not yet recommended for production use.

## Related Links

- [TNS-CSI GitHub](https://github.com/fenio/tns-csi)
- [Democratic-CSI GitHub](https://github.com/democratic-csi/democratic-csi)
- [Democratic-CSI Helm Charts](https://github.com/democratic-csi/charts)
