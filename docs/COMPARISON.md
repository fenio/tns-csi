# CSI Driver Comparison

This document compares TNS-CSI with other CSI drivers for TrueNAS.

**Last Updated**: January 2026

---

## TNS-CSI vs truenas-csi (Official TrueNAS CSI Driver)

The [official TrueNAS CSI driver](https://github.com/truenas/truenas-csi) was released by iXsystems in December 2025.

### Overview

| Aspect | TNS-CSI | truenas-csi (Official) |
|--------|---------|------------------------|
| **Maintainer** | Community (fenio) | iXsystems |
| **License** | Apache 2.0 | GPL-3.0 |
| **TrueNAS Version** | Scale 25.10+ | Scale 25.10+ |
| **API Communication** | WebSocket API | WebSocket API |
| **Language** | Go | Go |

### Protocol Support

| Protocol | TNS-CSI | truenas-csi |
|----------|---------|-------------|
| **NFS** | Yes | Yes |
| **iSCSI** | No (intentionally excluded) | Yes |
| **NVMe-oF (TCP)** | Yes | No |

**Philosophy difference**: TNS-CSI deliberately excludes iSCSI in favor of NVMe-oF, which offers better performance on modern networks (10GbE+). The official driver takes the opposite approach - supporting the mature iSCSI protocol but not NVMe-oF.

### Feature Comparison

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

### Unique to TNS-CSI

1. **kubectl Plugin (`kubectl tns-csi`)**
   - List managed volumes and snapshots
   - Find orphaned volumes (exist on TrueNAS but no matching PVC)
   - Discover and import unmanaged datasets
   - Volume adoption across cluster rebuilds
   - Health checks and troubleshooting
   - Migration assistance from democratic-csi

2. **Detached Snapshots**
   - Uses `zfs send/receive` to create independent dataset copies
   - Survives deletion of source volume
   - Useful for backup/DR scenarios

3. **Volume Adoption**
   - Mark volumes as "adoptable" for cluster migration
   - Import existing datasets into management
   - Re-adopt volumes after cluster rebuild

4. **Prometheus Metrics**
   - Volume operation latencies
   - Error rates by operation type
   - Volume capacity tracking

5. **NVMe-oF Support**
   - Modern block storage protocol
   - Better performance than iSCSI on fast networks
   - Lower CPU overhead

### Unique to truenas-csi (Official)

1. **Dataset Encryption**
   - AES-256-GCM and AES-128-CCM
   - Passphrase or hex key support
   - Automatic key generation

2. **Automatic Snapshot Scheduling**
   - Cron-based scheduling in StorageClass
   - Configurable retention policies
   - No external snapshot controller needed for scheduled snapshots

3. **iSCSI with CHAP**
   - Mature block storage protocol
   - CHAP authentication (including mutual)
   - Initiator IQN filtering
   - Network CIDR restrictions

4. **Official Support**
   - Maintained by iXsystems
   - Likely to have better long-term support
   - Integration with TrueNAS roadmap

### When to Choose Each

**Choose TNS-CSI if:**
- You want **NVMe-oF** for high-performance block storage
- You need **volume adoption/migration** features
- You want a **kubectl plugin** for volume management
- You're migrating from **democratic-csi** and want similar workflows
- You need **Prometheus metrics** for monitoring
- You want **detached snapshots** for backup/DR

**Choose truenas-csi (Official) if:**
- You need **iSCSI** (established infrastructure, compatibility requirements)
- You want **dataset encryption** at the ZFS level
- You need **automatic snapshot scheduling** without external tools
- You prefer **official vendor support**
- You want the safety of an **iXsystems-maintained** project

### Maturity

| Aspect | TNS-CSI | truenas-csi |
|--------|---------|-------------|
| **Project Age** | ~6 months | ~1 month (Dec 2025) |
| **Production Use** | Homelab tested | Unknown |
| **Test Coverage** | Unit + E2E tests | Unknown |

**Note**: The official truenas-csi is very new (created December 2025). While it has iXsystems backing, it may still have early-stage issues. TNS-CSI has been in development longer but lacks official vendor support.

---

## TNS-CSI vs Democratic-CSI

[Democratic-CSI](https://github.com/democratic-csi/democratic-csi) is the most popular community CSI driver for TrueNAS with 1.2k+ stars.

### Overview

| Aspect | TNS-CSI | Democratic-CSI |
|--------|---------|----------------|
| **Maturity** | Early development | Mature, established |
| **Language** | Go | JavaScript (Node.js) |
| **License** | Apache 2.0 | MIT |
| **TrueNAS Version** | Scale 25.10+ only | FreeNAS/TrueNAS (multiple versions) |
| **API Connection** | WebSocket API only (no SSH) | SSH-based or HTTP API (experimental) |

### Protocol Support

| Protocol | TNS-CSI | Democratic-CSI |
|----------|---------|----------------|
| **NFS** | Yes | Yes |
| **NVMe-oF** | Yes (primary block protocol) | Yes (zfs-generic-nvmeof driver) |
| **iSCSI** | No (by design) | Yes (primary block protocol) |
| **SMB/CIFS** | No (low priority) | Yes |

### Key Differences

#### Architecture Philosophy

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

#### Backend Support

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

### Feature Comparison

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
| kubectl plugin | Yes | No |
| Volume adoption | Yes | No |

### Configuration Complexity

**TNS-CSI:**
- Simpler configuration (fewer options)
- Helm chart or kubectl manifests
- No SSH setup required
- API key authentication only

**Democratic-CSI:**
- More complex configuration with many options
- Requires SSH setup and potentially sudo configuration for most drivers
- Experimental `freenas-api-*` drivers work without SSH (SCALE 21.08+)
- Helm chart with extensive example values
- May require shell configuration on TrueNAS

### When to Choose Each

**Choose TNS-CSI if:**
- You're running TrueNAS Scale 25.10+
- You want NVMe-oF for block storage (better performance than iSCSI)
- You prefer a simpler, focused driver with fewer moving parts
- You don't want to configure SSH access to your NAS
- You need volume health monitoring (ControllerGetVolume)
- You want comprehensive Prometheus metrics
- You need volume adoption/migration features
- You prefer native Go implementation

**Choose Democratic-CSI if:**
- You need production-ready, battle-tested software
- You're running older TrueNAS/FreeNAS versions or TrueNAS CORE
- You need iSCSI or SMB support
- You need Windows node support
- You want multi-backend flexibility (ZoL, Synology, ObjectiveFS, etc.)
- You need local/ephemeral volume support
- You need Nomad or Docker Swarm support

---

## Why NVMe-oF Over iSCSI?

TNS-CSI deliberately chose NVMe-oF as its block storage protocol instead of iSCSI:

- **Lower latency**: NVMe-oF has significantly lower protocol overhead
- **Higher IOPS**: Designed for modern NVMe SSDs and their parallel I/O capabilities
- **Simpler stack**: No SCSI translation layer
- **Future-proof**: NVMe-oF is the direction the industry is moving

For workloads that can benefit from high-performance block storage, NVMe-oF provides measurable improvements over iSCSI.

---

## Summary Comparison

| | TNS-CSI | truenas-csi (Official) | Democratic-CSI |
|---|---------|------------------------|----------------|
| **Best for** | Modern TrueNAS with NVMe-oF | iSCSI + encryption needs | Broad compatibility |
| **Block protocol** | NVMe-oF | iSCSI | iSCSI (+ NVMe-oF for ZoL) |
| **Unique strength** | kubectl plugin, metrics, adoption | Encryption, scheduled snapshots | Multi-backend, Windows |
| **Trade-off** | No iSCSI, no encryption | No NVMe-oF, no plugin | SSH complexity |
| **Maturity** | Early development | Very new (Dec 2025) | Mature, production-ready |

---

## Related Links

- [TNS-CSI GitHub](https://github.com/fenio/tns-csi)
- [truenas-csi GitHub](https://github.com/truenas/truenas-csi) (Official)
- [Democratic-CSI GitHub](https://github.com/democratic-csi/democratic-csi)
- [Democratic-CSI Helm Charts](https://github.com/democratic-csi/charts)
