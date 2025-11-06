# NVMe-oF Subsystem Design Issue and Fix

## Problem Identified

The current CSI driver implementation **creates a new NVMe-oF subsystem for each volume**. This is architecturally incorrect and causes several issues:

### Current (Incorrect) Behavior
1. User requests PVC
2. Driver creates:
   - ZVOL (the actual storage volume)
   - **NEW subsystem** (named after the PVC)
   - Port association for that subsystem
   - Namespace within that subsystem pointing to the ZVOL
3. When deletion fails, orphaned subsystems remain on TrueNAS
4. These orphaned subsystems show as "unusable" because they have no namespaces

### Why This is Wrong

**NVMe-oF Architecture:**
- A **subsystem** is a logical grouping that represents a storage target
- A subsystem has an NQN (NVMe Qualified Name) - the target address
- A subsystem can contain multiple **namespaces** (the actual volumes)
- Subsystems are associated with **ports** (TCP/RDMA endpoints)

**Correct Usage:**
- One subsystem should serve many namespaces (volumes)
- The subsystem and ports are infrastructure, managed by the admin
- The CSI driver should only manage namespaces (volumes) within pre-existing subsystems

**Analogy:**
- Subsystem = NFS Server (one server, many shares)
- Namespace = Individual volume/LUN
- Current driver behavior = Creating a new NFS server for each share (wasteful, wrong)

## Root Cause

Looking at `pkg/driver/controller_nvmeof.go:65-134`:

```go
// Step 2: Create NVMe-oF subsystem with allow_any_host enabled
subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, tnsapi.NVMeOFSubsystemCreateParams{
    Name:         volumeName,  // ❌ Creating subsystem per volume
    AllowAnyHost: true,
})

// Step 2b: Query for available NVMe-oF ports and attach subsystem to the TCP port
ports, err := s.apiClient.QueryNVMeOFPorts(ctx)
// ... attach subsystem to port ...

// Step 3: Create NVMe-oF namespace
namespace, err := s.apiClient.CreateNVMeOFNamespace(ctx, ...)
```

This creates subsystem → attaches to port → creates namespace for EACH volume.

## Correct Design

### User Responsibilities (Pre-Configuration)
1. Create ONE NVMe-oF subsystem in TrueNAS UI
2. Configure ports (TCP 4420) for that subsystem
3. Enable `allow_any_host` or configure specific host NQNs
4. Provide the subsystem NQN in StorageClass parameters

### CSI Driver Responsibilities
1. Query for the pre-configured subsystem by NQN
2. Create only the ZVOL (volume)
3. Create only the namespace within the existing subsystem
4. On delete: Remove namespace and ZVOL only (never delete subsystem)

### New StorageClass Parameters

```yaml
parameters:
  pool: "tank"
  server: "192.168.1.100"
  transport: "tcp"
  port: "4420"
  subsystemNQN: "nqn.2014-08.org.nvmexpress:uuid:12345678-1234-1234-1234-123456789012"  # NEW: Required
```

## Implementation Plan

### 1. Update `pkg/tnsapi/client.go`
Add method to query subsystems without filters (get all or by ID):
```go
func (c *Client) QueryAllNVMeOFSubsystems(ctx context.Context) ([]NVMeOFSubsystem, error)
func (c *Client) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error)
```

### 2. Update `pkg/driver/controller_nvmeof.go`

**In `createNVMeOFVolume`:**
- Remove subsystem creation logic
- Remove port attachment logic
- Add subsystem query by NQN from StorageClass parameters
- Keep only ZVOL creation and namespace creation
- Return error if subsystem NQN not provided or not found

**In `deleteNVMeOFVolume`:**
- Remove subsystem deletion logic
- Keep only namespace deletion and ZVOL deletion

**In `setupNVMeOFVolumeFromClone`:**
- Same changes as `createNVMeOFVolume`

### 3. Update StorageClass Templates
- Add `subsystemNQN` parameter to Helm values
- Update documentation with setup instructions

### 4. Update Cleanup Script
The cleanup script (`scripts/cleanup-truenas-artifacts.sh`) should:
- Remove orphaned namespaces (within the configured subsystem)
- Remove orphaned ZVOLs
- **Never delete the subsystem itself**

### 5. Update Documentation
- Document NVMe-oF subsystem pre-configuration steps
- Explain subsystem vs namespace architecture
- Provide TrueNAS UI screenshots for subsystem setup
- Update quickstart guides

## Migration Path for Existing Deployments

For users who already have volumes created with the old (broken) approach:

1. **Before upgrading:**
   - Backup all PVC data
   - Document existing PVCs
   
2. **Manual cleanup:**
   - Delete all PVCs (this will attempt cleanup)
   - Manually delete orphaned subsystems from TrueNAS UI
   - Create one shared subsystem with ports
   
3. **After upgrading:**
   - Update StorageClass with `subsystemNQN` parameter
   - Recreate PVCs (they will use the shared subsystem)
   - Restore data from backups

## Benefits of This Fix

1. **Correctness**: Follows NVMe-oF architecture correctly
2. **Scalability**: One subsystem can serve thousands of namespaces
3. **Cleaner TrueNAS**: No subsystem proliferation
4. **Easier cleanup**: Orphaned namespaces are easier to manage
5. **Performance**: Less overhead in TrueNAS NVMe target
6. **Alignment with expectations**: User configures infra, driver manages volumes

## Testing Requirements

1. Test volume creation with pre-configured subsystem
2. Test volume deletion (verify subsystem remains)
3. Test multiple volumes sharing one subsystem
4. Test error handling when subsystem NQN is missing/invalid
5. Test snapshot and clone operations with new design
6. Test cleanup script (should not touch subsystem)
7. Integration test updates to pre-create subsystem

## Timeline

This is a **breaking change** that requires:
- Code changes (2-3 hours)
- Documentation updates (1-2 hours)
- Testing (2-3 hours)
- User migration guide (1 hour)

Total: ~1 day of focused work

## Version Impact

This should be released as:
- **Major version bump** (breaking change)
- Or clearly marked as **breaking change** in release notes
- Provide migration guide and deprecation notice
