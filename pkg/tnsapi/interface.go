// Package tnsapi provides a WebSocket client for TrueNAS Scale API.
package tnsapi

import (
	"context"
	"time"
)

// ClientInterface defines the interface for TrueNAS API operations.
// This allows for dependency injection and easier testing.
//
//nolint:interfacebloat // TrueNAS API client naturally has many methods covering different resource types
type ClientInterface interface {
	// Pool operations
	QueryPool(ctx context.Context, poolName string) (*Pool, error)

	// Dataset operations
	CreateDataset(ctx context.Context, params DatasetCreateParams) (*Dataset, error)
	DeleteDataset(ctx context.Context, datasetID string) error
	Dataset(ctx context.Context, datasetID string) (*Dataset, error)
	UpdateDataset(ctx context.Context, datasetID string, params DatasetUpdateParams) (*Dataset, error)
	QueryAllDatasets(ctx context.Context, prefix string) ([]Dataset, error)

	// ZFS User Property operations (for CSI metadata tracking)
	SetSnapshotProperties(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error
	SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error
	GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error)
	GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error)
	InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error
	ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error

	// Dataset lookup by ZFS user properties (for volume recovery and orphan detection)
	FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]DatasetWithProperties, error)
	FindManagedDatasets(ctx context.Context, prefix string) ([]DatasetWithProperties, error)
	FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*DatasetWithProperties, error)

	// NFS share operations
	CreateNFSShare(ctx context.Context, params NFSShareCreateParams) (*NFSShare, error)
	DeleteNFSShare(ctx context.Context, shareID int) error
	QueryNFSShare(ctx context.Context, path string) ([]NFSShare, error)
	QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]NFSShare, error)

	// ZVOL operations
	CreateZvol(ctx context.Context, params ZvolCreateParams) (*Dataset, error)

	// NVMe-oF operations
	CreateNVMeOFSubsystem(ctx context.Context, params NVMeOFSubsystemCreateParams) (*NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error
	NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error)
	QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]NVMeOFSubsystem, error)
	ListAllNVMeOFSubsystems(ctx context.Context) ([]NVMeOFSubsystem, error)

	CreateNVMeOFNamespace(ctx context.Context, params NVMeOFNamespaceCreateParams) (*NVMeOFNamespace, error)
	DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error
	QueryAllNVMeOFNamespaces(ctx context.Context) ([]NVMeOFNamespace, error)

	AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error
	RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error
	QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]NVMeOFPortSubsystem, error)
	QueryNVMeOFPorts(ctx context.Context) ([]NVMeOFPort, error)

	// Snapshot operations
	CreateSnapshot(ctx context.Context, params SnapshotCreateParams) (*Snapshot, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) error
	QuerySnapshots(ctx context.Context, filters []interface{}) ([]Snapshot, error)
	CloneSnapshot(ctx context.Context, params CloneSnapshotParams) (*Dataset, error)

	// Dataset promotion (for detached clones)
	// PromoteDataset promotes a cloned dataset to become independent from its origin snapshot.
	// This breaks the parent-child relationship, making the clone a standalone dataset.
	PromoteDataset(ctx context.Context, datasetID string) error

	// Replication operations (for detached snapshots)
	// RunOnetimeReplication runs a one-time zfs send/receive operation.
	// Returns the job ID for tracking the operation status.
	RunOnetimeReplication(ctx context.Context, params ReplicationRunOnetimeParams) (int, error)

	// GetJobStatus retrieves the status of a job by its ID.
	GetJobStatus(ctx context.Context, jobID int) (*ReplicationJobState, error)

	// WaitForJob waits for a job to complete with polling.
	WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error

	// RunOnetimeReplicationAndWait runs a one-time replication and waits for completion.
	RunOnetimeReplicationAndWait(ctx context.Context, params ReplicationRunOnetimeParams, pollInterval time.Duration) error

	// Connection management
	Close()
}

// Verify that Client implements ClientInterface at compile time.
var _ ClientInterface = (*Client)(nil)
