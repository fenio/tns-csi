package main

import (
	"context"
	"errors"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// Compile-time verification that mockClient implements tnsapi.ClientInterface.
var _ tnsapi.ClientInterface = (*mockClient)(nil)

// mockClient is a function-injection mock implementing tnsapi.ClientInterface.
// Each method has an optional func field; if nil, returns a default error.
type mockClient struct {
	// Pool operations
	QueryPoolFunc func(ctx context.Context, poolName string) (*tnsapi.Pool, error)

	// Dataset operations
	CreateDatasetFunc    func(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error)
	DeleteDatasetFunc    func(ctx context.Context, datasetID string) error
	DatasetFunc          func(ctx context.Context, datasetID string) (*tnsapi.Dataset, error)
	UpdateDatasetFunc    func(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error)
	QueryAllDatasetsFunc func(ctx context.Context, prefix string) ([]tnsapi.Dataset, error)

	// ZFS User Property operations
	SetSnapshotPropertiesFunc   func(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error
	SetDatasetPropertiesFunc    func(ctx context.Context, datasetID string, properties map[string]string) error
	GetDatasetPropertiesFunc    func(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error)
	GetAllDatasetPropertiesFunc func(ctx context.Context, datasetID string) (map[string]string, error)
	InheritDatasetPropertyFunc  func(ctx context.Context, datasetID, propertyName string) error
	ClearDatasetPropertiesFunc  func(ctx context.Context, datasetID string, propertyNames []string) error

	// Dataset lookup by ZFS user properties
	GetDatasetWithPropertiesFunc   func(ctx context.Context, datasetID string) (*tnsapi.DatasetWithProperties, error)
	FindDatasetsByPropertyFunc     func(ctx context.Context, prefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error)
	FindManagedDatasetsFunc        func(ctx context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error)
	FindDatasetByCSIVolumeNameFunc func(ctx context.Context, prefix, csiVolumeName string) (*tnsapi.DatasetWithProperties, error)

	// NFS share operations
	CreateNFSShareFunc    func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShareFunc    func(ctx context.Context, shareID int) error
	QueryNFSShareFunc     func(ctx context.Context, path string) ([]tnsapi.NFSShare, error)
	QueryNFSShareByIDFunc func(ctx context.Context, shareID int) (*tnsapi.NFSShare, error)
	QueryAllNFSSharesFunc func(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error)

	// ZVOL operations
	CreateZvolFunc func(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error)

	// NVMe-oF operations
	CreateNVMeOFSubsystemFunc   func(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc   func(ctx context.Context, subsystemID int) error
	NVMeOFSubsystemByNQNFunc    func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error)
	QueryNVMeOFSubsystemFunc    func(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error)
	ListAllNVMeOFSubsystemsFunc func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error)

	CreateNVMeOFNamespaceFunc    func(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error)
	DeleteNVMeOFNamespaceFunc    func(ctx context.Context, namespaceID int) error
	QueryNVMeOFNamespaceByIDFunc func(ctx context.Context, namespaceID int) (*tnsapi.NVMeOFNamespace, error)
	QueryAllNVMeOFNamespacesFunc func(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error)

	AddSubsystemToPortFunc         func(ctx context.Context, subsystemID, portID int) error
	RemoveSubsystemFromPortFunc    func(ctx context.Context, portSubsysID int) error
	QuerySubsystemPortBindingsFunc func(ctx context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error)
	QueryNVMeOFPortsFunc           func(ctx context.Context) ([]tnsapi.NVMeOFPort, error)

	// iSCSI operations
	GetISCSIGlobalConfigFunc func(ctx context.Context) (*tnsapi.ISCSIGlobalConfig, error)
	QueryISCSIPortalsFunc    func(ctx context.Context) ([]tnsapi.ISCSIPortal, error)
	QueryISCSIInitiatorsFunc func(ctx context.Context) ([]tnsapi.ISCSIInitiator, error)

	CreateISCSITargetFunc func(ctx context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error)
	DeleteISCSITargetFunc func(ctx context.Context, targetID int, force bool) error
	QueryISCSITargetsFunc func(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITarget, error)
	ISCSITargetByNameFunc func(ctx context.Context, name string) (*tnsapi.ISCSITarget, error)

	CreateISCSIExtentFunc func(ctx context.Context, params tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error)
	DeleteISCSIExtentFunc func(ctx context.Context, extentID int, removeFile, force bool) error
	QueryISCSIExtentsFunc func(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSIExtent, error)
	ISCSIExtentByNameFunc func(ctx context.Context, name string) (*tnsapi.ISCSIExtent, error)

	CreateISCSITargetExtentFunc   func(ctx context.Context, params tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error)
	DeleteISCSITargetExtentFunc   func(ctx context.Context, targetExtentID int, force bool) error
	QueryISCSITargetExtentsFunc   func(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITargetExtent, error)
	ISCSITargetExtentByTargetFunc func(ctx context.Context, targetID int) ([]tnsapi.ISCSITargetExtent, error)

	// iSCSI service management
	ReloadISCSIServiceFunc func(ctx context.Context) error

	// Snapshot operations
	CreateSnapshotFunc   func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error)
	DeleteSnapshotFunc   func(ctx context.Context, snapshotID string) error
	QuerySnapshotsFunc   func(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error)
	QuerySnapshotIDsFunc func(ctx context.Context, filters []interface{}) ([]string, error)
	CloneSnapshotFunc    func(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error)

	// Dataset promotion
	PromoteDatasetFunc func(ctx context.Context, datasetID string) error

	// Replication operations
	RunOnetimeReplicationFunc        func(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error)
	GetJobStatusFunc                 func(ctx context.Context, jobID int) (*tnsapi.ReplicationJobState, error)
	WaitForJobFunc                   func(ctx context.Context, jobID int, pollInterval time.Duration) error
	RunOnetimeReplicationAndWaitFunc func(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams, pollInterval time.Duration) error
}

// errNotImplemented is the default error returned when a mock function is not set.
var errNotImplemented = errors.New("not implemented in mock")

// Pool operations.

func (m *mockClient) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	if m.QueryPoolFunc != nil {
		return m.QueryPoolFunc(ctx, poolName)
	}
	return nil, errNotImplemented
}

// Dataset operations.

func (m *mockClient) CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	if m.CreateDatasetFunc != nil {
		return m.CreateDatasetFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteDataset(ctx context.Context, datasetID string) error {
	if m.DeleteDatasetFunc != nil {
		return m.DeleteDatasetFunc(ctx, datasetID)
	}
	return errNotImplemented
}

func (m *mockClient) Dataset(ctx context.Context, datasetID string) (*tnsapi.Dataset, error) {
	if m.DatasetFunc != nil {
		return m.DatasetFunc(ctx, datasetID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
	if m.UpdateDatasetFunc != nil {
		return m.UpdateDatasetFunc(ctx, datasetID, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryAllDatasets(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
	if m.QueryAllDatasetsFunc != nil {
		return m.QueryAllDatasetsFunc(ctx, prefix)
	}
	return nil, errNotImplemented
}

// ZFS User Property operations.

func (m *mockClient) SetSnapshotProperties(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error {
	if m.SetSnapshotPropertiesFunc != nil {
		return m.SetSnapshotPropertiesFunc(ctx, snapshotID, updateProperties, removeProperties)
	}
	return errNotImplemented
}

func (m *mockClient) SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error {
	if m.SetDatasetPropertiesFunc != nil {
		return m.SetDatasetPropertiesFunc(ctx, datasetID, properties)
	}
	return errNotImplemented
}

func (m *mockClient) GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error) {
	if m.GetDatasetPropertiesFunc != nil {
		return m.GetDatasetPropertiesFunc(ctx, datasetID, propertyNames)
	}
	return nil, errNotImplemented
}

func (m *mockClient) GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error) {
	if m.GetAllDatasetPropertiesFunc != nil {
		return m.GetAllDatasetPropertiesFunc(ctx, datasetID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error {
	if m.InheritDatasetPropertyFunc != nil {
		return m.InheritDatasetPropertyFunc(ctx, datasetID, propertyName)
	}
	return errNotImplemented
}

func (m *mockClient) ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error {
	if m.ClearDatasetPropertiesFunc != nil {
		return m.ClearDatasetPropertiesFunc(ctx, datasetID, propertyNames)
	}
	return errNotImplemented
}

// Dataset lookup by ZFS user properties.

func (m *mockClient) GetDatasetWithProperties(ctx context.Context, datasetID string) (*tnsapi.DatasetWithProperties, error) {
	if m.GetDatasetWithPropertiesFunc != nil {
		return m.GetDatasetWithPropertiesFunc(ctx, datasetID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error) {
	if m.FindDatasetsByPropertyFunc != nil {
		return m.FindDatasetsByPropertyFunc(ctx, prefix, propertyName, propertyValue)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindManagedDatasets(ctx context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error) {
	if m.FindManagedDatasetsFunc != nil {
		return m.FindManagedDatasetsFunc(ctx, prefix)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*tnsapi.DatasetWithProperties, error) {
	if m.FindDatasetByCSIVolumeNameFunc != nil {
		return m.FindDatasetByCSIVolumeNameFunc(ctx, prefix, csiVolumeName)
	}
	return nil, errNotImplemented
}

// NFS share operations.

func (m *mockClient) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	if m.CreateNFSShareFunc != nil {
		return m.CreateNFSShareFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteNFSShare(ctx context.Context, shareID int) error {
	if m.DeleteNFSShareFunc != nil {
		return m.DeleteNFSShareFunc(ctx, shareID)
	}
	return errNotImplemented
}

func (m *mockClient) QueryNFSShare(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
	if m.QueryNFSShareFunc != nil {
		return m.QueryNFSShareFunc(ctx, path)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryNFSShareByID(ctx context.Context, shareID int) (*tnsapi.NFSShare, error) {
	if m.QueryNFSShareByIDFunc != nil {
		return m.QueryNFSShareByIDFunc(ctx, shareID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
	if m.QueryAllNFSSharesFunc != nil {
		return m.QueryAllNFSSharesFunc(ctx, pathPrefix)
	}
	return nil, errNotImplemented
}

// SMB share operations.

func (m *mockClient) CreateSMBShare(ctx context.Context, params tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
	return nil, errNotImplemented
}

func (m *mockClient) DeleteSMBShare(ctx context.Context, shareID int) error {
	return errNotImplemented
}

func (m *mockClient) QuerySMBShare(ctx context.Context, path string) ([]tnsapi.SMBShare, error) {
	return nil, errNotImplemented
}

func (m *mockClient) QuerySMBShareByID(ctx context.Context, shareID int) (*tnsapi.SMBShare, error) {
	return nil, errNotImplemented
}

func (m *mockClient) QueryAllSMBShares(ctx context.Context, pathPrefix string) ([]tnsapi.SMBShare, error) {
	return nil, errNotImplemented
}

func (m *mockClient) FilesystemStat(ctx context.Context, path string) error {
	return nil
}

func (m *mockClient) SetFilesystemACL(ctx context.Context, path string) error {
	return nil
}

// ZVOL operations.

func (m *mockClient) CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
	if m.CreateZvolFunc != nil {
		return m.CreateZvolFunc(ctx, params)
	}
	return nil, errNotImplemented
}

// NVMe-oF operations.

func (m *mockClient) CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	if m.CreateNVMeOFSubsystemFunc != nil {
		return m.CreateNVMeOFSubsystemFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error {
	if m.DeleteNVMeOFSubsystemFunc != nil {
		return m.DeleteNVMeOFSubsystemFunc(ctx, subsystemID)
	}
	return errNotImplemented
}

func (m *mockClient) NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	if m.NVMeOFSubsystemByNQNFunc != nil {
		return m.NVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error) {
	if m.QueryNVMeOFSubsystemFunc != nil {
		return m.QueryNVMeOFSubsystemFunc(ctx, nqn)
	}
	return nil, errNotImplemented
}

func (m *mockClient) ListAllNVMeOFSubsystems(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	if m.ListAllNVMeOFSubsystemsFunc != nil {
		return m.ListAllNVMeOFSubsystemsFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
	if m.CreateNVMeOFNamespaceFunc != nil {
		return m.CreateNVMeOFNamespaceFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error {
	if m.DeleteNVMeOFNamespaceFunc != nil {
		return m.DeleteNVMeOFNamespaceFunc(ctx, namespaceID)
	}
	return errNotImplemented
}

func (m *mockClient) QueryNVMeOFNamespaceByID(ctx context.Context, namespaceID int) (*tnsapi.NVMeOFNamespace, error) {
	if m.QueryNVMeOFNamespaceByIDFunc != nil {
		return m.QueryNVMeOFNamespaceByIDFunc(ctx, namespaceID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryAllNVMeOFNamespaces(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
	if m.QueryAllNVMeOFNamespacesFunc != nil {
		return m.QueryAllNVMeOFNamespacesFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error {
	if m.AddSubsystemToPortFunc != nil {
		return m.AddSubsystemToPortFunc(ctx, subsystemID, portID)
	}
	return errNotImplemented
}

func (m *mockClient) RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error {
	if m.RemoveSubsystemFromPortFunc != nil {
		return m.RemoveSubsystemFromPortFunc(ctx, portSubsysID)
	}
	return errNotImplemented
}

func (m *mockClient) QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error) {
	if m.QuerySubsystemPortBindingsFunc != nil {
		return m.QuerySubsystemPortBindingsFunc(ctx, subsystemID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryNVMeOFPorts(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
	if m.QueryNVMeOFPortsFunc != nil {
		return m.QueryNVMeOFPortsFunc(ctx)
	}
	return nil, errNotImplemented
}

// iSCSI operations.

func (m *mockClient) GetISCSIGlobalConfig(ctx context.Context) (*tnsapi.ISCSIGlobalConfig, error) {
	if m.GetISCSIGlobalConfigFunc != nil {
		return m.GetISCSIGlobalConfigFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryISCSIPortals(ctx context.Context) ([]tnsapi.ISCSIPortal, error) {
	if m.QueryISCSIPortalsFunc != nil {
		return m.QueryISCSIPortalsFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QueryISCSIInitiators(ctx context.Context) ([]tnsapi.ISCSIInitiator, error) {
	if m.QueryISCSIInitiatorsFunc != nil {
		return m.QueryISCSIInitiatorsFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) CreateISCSITarget(ctx context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
	if m.CreateISCSITargetFunc != nil {
		return m.CreateISCSITargetFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteISCSITarget(ctx context.Context, targetID int, force bool) error {
	if m.DeleteISCSITargetFunc != nil {
		return m.DeleteISCSITargetFunc(ctx, targetID, force)
	}
	return errNotImplemented
}

func (m *mockClient) QueryISCSITargets(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITarget, error) {
	if m.QueryISCSITargetsFunc != nil {
		return m.QueryISCSITargetsFunc(ctx, filters)
	}
	return nil, errNotImplemented
}

func (m *mockClient) ISCSITargetByName(ctx context.Context, name string) (*tnsapi.ISCSITarget, error) {
	if m.ISCSITargetByNameFunc != nil {
		return m.ISCSITargetByNameFunc(ctx, name)
	}
	return nil, errNotImplemented
}

func (m *mockClient) CreateISCSIExtent(ctx context.Context, params tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
	if m.CreateISCSIExtentFunc != nil {
		return m.CreateISCSIExtentFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteISCSIExtent(ctx context.Context, extentID int, removeFile, force bool) error {
	if m.DeleteISCSIExtentFunc != nil {
		return m.DeleteISCSIExtentFunc(ctx, extentID, removeFile, force)
	}
	return errNotImplemented
}

func (m *mockClient) QueryISCSIExtents(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSIExtent, error) {
	if m.QueryISCSIExtentsFunc != nil {
		return m.QueryISCSIExtentsFunc(ctx, filters)
	}
	return nil, errNotImplemented
}

func (m *mockClient) ISCSIExtentByName(ctx context.Context, name string) (*tnsapi.ISCSIExtent, error) {
	if m.ISCSIExtentByNameFunc != nil {
		return m.ISCSIExtentByNameFunc(ctx, name)
	}
	return nil, errNotImplemented
}

func (m *mockClient) CreateISCSITargetExtent(ctx context.Context, params tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
	if m.CreateISCSITargetExtentFunc != nil {
		return m.CreateISCSITargetExtentFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteISCSITargetExtent(ctx context.Context, targetExtentID int, force bool) error {
	if m.DeleteISCSITargetExtentFunc != nil {
		return m.DeleteISCSITargetExtentFunc(ctx, targetExtentID, force)
	}
	return errNotImplemented
}

func (m *mockClient) QueryISCSITargetExtents(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITargetExtent, error) {
	if m.QueryISCSITargetExtentsFunc != nil {
		return m.QueryISCSITargetExtentsFunc(ctx, filters)
	}
	return nil, errNotImplemented
}

func (m *mockClient) ISCSITargetExtentByTarget(ctx context.Context, targetID int) ([]tnsapi.ISCSITargetExtent, error) {
	if m.ISCSITargetExtentByTargetFunc != nil {
		return m.ISCSITargetExtentByTargetFunc(ctx, targetID)
	}
	return nil, errNotImplemented
}

// iSCSI service management.

func (m *mockClient) ReloadISCSIService(ctx context.Context) error {
	if m.ReloadISCSIServiceFunc != nil {
		return m.ReloadISCSIServiceFunc(ctx)
	}
	return errNotImplemented
}

func (m *mockClient) ReloadSMBService(_ context.Context) error {
	return nil
}

func (m *mockClient) UpdateSMBShare(_ context.Context, _ int, _ tnsapi.SMBShareUpdateParams) (*tnsapi.SMBShare, error) {
	return &tnsapi.SMBShare{}, nil
}

// Snapshot operations.

func (m *mockClient) CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, snapshotID)
	}
	return errNotImplemented
}

func (m *mockClient) QuerySnapshots(ctx context.Context, filters []interface{}) ([]tnsapi.Snapshot, error) {
	if m.QuerySnapshotsFunc != nil {
		return m.QuerySnapshotsFunc(ctx, filters)
	}
	return nil, errNotImplemented
}

func (m *mockClient) QuerySnapshotIDs(ctx context.Context, filters []interface{}) ([]string, error) {
	if m.QuerySnapshotIDsFunc != nil {
		return m.QuerySnapshotIDsFunc(ctx, filters)
	}
	return nil, errNotImplemented
}

func (m *mockClient) CloneSnapshot(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
	if m.CloneSnapshotFunc != nil {
		return m.CloneSnapshotFunc(ctx, params)
	}
	return nil, errNotImplemented
}

// Dataset promotion.

func (m *mockClient) PromoteDataset(ctx context.Context, datasetID string) error {
	if m.PromoteDatasetFunc != nil {
		return m.PromoteDatasetFunc(ctx, datasetID)
	}
	return errNotImplemented
}

// Replication operations.

func (m *mockClient) RunOnetimeReplication(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error) {
	if m.RunOnetimeReplicationFunc != nil {
		return m.RunOnetimeReplicationFunc(ctx, params)
	}
	return 0, errNotImplemented
}

func (m *mockClient) GetJobStatus(ctx context.Context, jobID int) (*tnsapi.ReplicationJobState, error) {
	if m.GetJobStatusFunc != nil {
		return m.GetJobStatusFunc(ctx, jobID)
	}
	return nil, errNotImplemented
}

func (m *mockClient) WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error {
	if m.WaitForJobFunc != nil {
		return m.WaitForJobFunc(ctx, jobID, pollInterval)
	}
	return errNotImplemented
}

func (m *mockClient) RunOnetimeReplicationAndWait(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams, pollInterval time.Duration) error {
	if m.RunOnetimeReplicationAndWaitFunc != nil {
		return m.RunOnetimeReplicationAndWaitFunc(ctx, params, pollInterval)
	}
	return errNotImplemented
}

// Connection management.

func (m *mockClient) Close() {
	// Mock client does not need cleanup.
}
