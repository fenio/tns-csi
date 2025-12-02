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
	PromoteDataset(ctx context.Context, datasetID string) error
	DatasetDestroySnapshots(ctx context.Context, datasetID string) error

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
	QueryNVMeOFPorts(ctx context.Context) ([]NVMeOFPort, error)

	// Snapshot operations
	CreateSnapshot(ctx context.Context, params SnapshotCreateParams) (*Snapshot, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) error
	QuerySnapshots(ctx context.Context, filters []interface{}) ([]Snapshot, error)
	CloneSnapshot(ctx context.Context, params CloneSnapshotParams) (*Dataset, error)
	CreateDetachedClone(ctx context.Context, snapshotID, targetDataset string, timeout time.Duration) (*Dataset, error)

	// Job operations (for async tasks like replication)
	CoreGetJobs(ctx context.Context, filters []interface{}) ([]Job, error)
	CoreGetJob(ctx context.Context, jobID int) (*Job, error)
	CoreWaitForJob(ctx context.Context, jobID int, timeout time.Duration) (*Job, error)

	// Replication operations (for detached snapshots/clones)
	ReplicationRunOnetime(ctx context.Context, params ReplicationRunOnetimeParams) (int, error)

	// Connection management
	Close()
}

// Verify that Client implements ClientInterface at compile time.
var _ ClientInterface = (*Client)(nil)
