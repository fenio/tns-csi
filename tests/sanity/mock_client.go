// Package sanity provides mock implementations for CSI sanity testing.
package sanity

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

var (
	// ErrDatasetExists indicates a dataset already exists.
	ErrDatasetExists = errors.New("dataset already exists")
	// ErrDatasetNotFound indicates a dataset was not found.
	ErrDatasetNotFound = errors.New("dataset not found")
	// ErrNFSShareNotFound indicates an NFS share was not found.
	ErrNFSShareNotFound = errors.New("NFS share not found")
	// ErrNVMeOFTargetNotFound indicates an NVMe-oF target was not found.
	ErrNVMeOFTargetNotFound = errors.New("NVMe-oF target not found")
	// ErrSnapshotNotFound indicates a snapshot was not found.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrSubsystemNotFound indicates a subsystem was not found.
	ErrSubsystemNotFound = errors.New("subsystem not found")
)

// MockClient is a mock implementation of the TrueNAS API client for sanity testing.
type MockClient struct {
	datasets        map[string]mockDataset
	nfsShares       map[int]mockNFSShare
	nvmeofTargets   map[int]mockNVMeOFTarget
	snapshots       map[string]mockSnapshot
	subsystems      map[string]mockSubsystem
	namespaces      map[int]mockNamespace
	callLog         []string
	nextDatasetID   int
	nextShareID     int
	nextTargetID    int
	nextSnapshotID  int
	nextSubsystemID int
	nextNamespaceID int
	mu              sync.Mutex
}

type mockDataset struct {
	ID         string
	Name       string
	Type       string
	Used       map[string]any
	Available  map[string]any
	Mountpoint string
	Volsize    int64
}

type mockNFSShare struct {
	Path    string
	Comment string
	ID      int
	Enabled bool
}

type mockNVMeOFTarget struct {
	NQN         string
	DevicePath  string
	ID          int
	SubsystemID int
	NamespaceID int
}

type mockSnapshot struct {
	ID      string
	Name    string
	Dataset string
}

type mockSubsystem struct {
	Name string
	NQN  string
	ID   int
}

type mockNamespace struct {
	Device      string
	ID          int
	SubsystemID int
	NSID        int
}

// NewMockClient creates a new mock TrueNAS API client.
func NewMockClient() *MockClient {
	return &MockClient{
		datasets:        make(map[string]mockDataset),
		nfsShares:       make(map[int]mockNFSShare),
		nvmeofTargets:   make(map[int]mockNVMeOFTarget),
		snapshots:       make(map[string]mockSnapshot),
		subsystems:      make(map[string]mockSubsystem),
		namespaces:      make(map[int]mockNamespace),
		nextDatasetID:   1,
		nextShareID:     1,
		nextTargetID:    1,
		nextSnapshotID:  1,
		nextSubsystemID: 1,
		nextNamespaceID: 1,
		callLog:         make([]string, 0),
	}
}

// logCall records an API call for debugging.
func (m *MockClient) logCall(method string, params ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callLog = append(m.callLog, fmt.Sprintf("%s(%v)", method, params))
}

// QueryPool mocks pool.query for capacity information.
func (m *MockClient) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	m.logCall("QueryPool", poolName)

	// Return mock pool with realistic capacity values
	return &tnsapi.Pool{
		ID:   1,
		Name: poolName,
		Properties: struct {
			Size struct {
				Parsed int64 `json:"parsed"`
			} `json:"size"`
			Allocated struct {
				Parsed int64 `json:"parsed"`
			} `json:"allocated"`
			Free struct {
				Parsed int64 `json:"parsed"`
			} `json:"free"`
			Capacity struct {
				Parsed int64 `json:"parsed"`
			} `json:"capacity"`
		}{
			Size: struct {
				Parsed int64 `json:"parsed"`
			}{Parsed: 1099511627776}, // 1TB total
			Allocated: struct {
				Parsed int64 `json:"parsed"`
			}{Parsed: 107374182400}, // 100GB used
			Free: struct {
				Parsed int64 `json:"parsed"`
			}{Parsed: 992137445376}, // 924GB available
			Capacity: struct {
				Parsed int64 `json:"parsed"`
			}{Parsed: 10}, // 10% used
		},
	}, nil
}

// CreateDataset mocks pool.dataset.create.
func (m *MockClient) CreateDataset(ctx context.Context, params tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) {
	m.logCall("CreateDataset", params.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.datasets[params.Name]; exists {
		return nil, fmt.Errorf("dataset %s: %w", params.Name, ErrDatasetExists)
	}

	datasetID := fmt.Sprintf("dataset-%d", m.nextDatasetID)
	m.nextDatasetID++

	m.datasets[params.Name] = mockDataset{
		ID:         datasetID,
		Name:       params.Name,
		Type:       params.Type,
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)}, // 100GB
		Mountpoint: "/mnt/" + params.Name,
	}

	return &tnsapi.Dataset{
		ID:         datasetID,
		Name:       params.Name,
		Type:       params.Type,
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.Name,
	}, nil
}

// DeleteDataset mocks pool.dataset.delete.
func (m *MockClient) DeleteDataset(ctx context.Context, id string) error {
	m.logCall("DeleteDataset", id)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID
	for name, ds := range m.datasets {
		if ds.ID == id || ds.Name == id {
			delete(m.datasets, name)
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", id, ErrDatasetNotFound)
}

// GetDataset mocks pool.dataset.query.
func (m *MockClient) GetDataset(ctx context.Context, name string) (*tnsapi.Dataset, error) {
	m.logCall("GetDataset", name)

	m.mu.Lock()
	defer m.mu.Unlock()

	ds, exists := m.datasets[name]
	if !exists {
		return nil, fmt.Errorf("dataset %s: %w", name, ErrDatasetNotFound)
	}

	return &tnsapi.Dataset{
		ID:         ds.ID,
		Name:       ds.Name,
		Type:       ds.Type,
		Used:       ds.Used,
		Available:  ds.Available,
		Mountpoint: ds.Mountpoint,
		Volsize:    map[string]interface{}{"parsed": float64(ds.Volsize)},
	}, nil
}

// UpdateDataset mocks pool.dataset.update.
func (m *MockClient) UpdateDataset(ctx context.Context, datasetID string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
	m.logCall("UpdateDataset", datasetID, params)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for name, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			// Update volsize if provided
			if params.Volsize != nil {
				ds.Volsize = *params.Volsize
				m.datasets[name] = ds
			}
			return &tnsapi.Dataset{
				ID:         ds.ID,
				Name:       ds.Name,
				Type:       ds.Type,
				Used:       ds.Used,
				Available:  ds.Available,
				Mountpoint: ds.Mountpoint,
				Volsize:    map[string]interface{}{"parsed": float64(ds.Volsize)},
			}, nil
		}
	}

	return nil, fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// QueryAllDatasets mocks pool.dataset.query (all datasets).
func (m *MockClient) QueryAllDatasets(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
	m.logCall("QueryAllDatasets", prefix)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.Dataset
	for _, ds := range m.datasets {
		if prefix == "" || len(ds.Name) >= len(prefix) && ds.Name[:len(prefix)] == prefix {
			result = append(result, tnsapi.Dataset{
				ID:         ds.ID,
				Name:       ds.Name,
				Type:       ds.Type,
				Used:       ds.Used,
				Available:  ds.Available,
				Mountpoint: ds.Mountpoint,
				Volsize:    map[string]interface{}{"parsed": float64(ds.Volsize)},
			})
		}
	}

	return result, nil
}

// CreateNFSShare mocks sharing.nfs.create.
func (m *MockClient) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	m.logCall("CreateNFSShare", params.Path, params.Comment)

	m.mu.Lock()
	defer m.mu.Unlock()

	shareID := m.nextShareID
	m.nextShareID++

	m.nfsShares[shareID] = mockNFSShare{
		ID:      shareID,
		Path:    params.Path,
		Comment: params.Comment,
		Enabled: params.Enabled,
	}

	return &tnsapi.NFSShare{
		ID:      shareID,
		Path:    params.Path,
		Comment: params.Comment,
		Enabled: params.Enabled,
	}, nil
}

// DeleteNFSShare mocks sharing.nfs.delete.
func (m *MockClient) DeleteNFSShare(ctx context.Context, id int) error {
	m.logCall("DeleteNFSShare", id)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.nfsShares[id]; !exists {
		return fmt.Errorf("NFS share %d: %w", id, ErrNFSShareNotFound)
	}

	delete(m.nfsShares, id)
	return nil
}

// QueryNFSShare mocks sharing.nfs.query by path.
func (m *MockClient) QueryNFSShare(ctx context.Context, path string) ([]tnsapi.NFSShare, error) {
	m.logCall("QueryNFSShare", path)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.NFSShare
	for _, share := range m.nfsShares {
		if share.Path == path {
			result = append(result, tnsapi.NFSShare{
				ID:      share.ID,
				Path:    share.Path,
				Comment: share.Comment,
				Enabled: share.Enabled,
			})
		}
	}

	return result, nil
}

// QueryAllNFSShares mocks sharing.nfs.query (all shares).
func (m *MockClient) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
	m.logCall("QueryAllNFSShares", pathPrefix)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.NFSShare
	for _, share := range m.nfsShares {
		if pathPrefix == "" || len(share.Path) >= len(pathPrefix) && share.Path[:len(pathPrefix)] == pathPrefix {
			result = append(result, tnsapi.NFSShare{
				ID:      share.ID,
				Path:    share.Path,
				Comment: share.Comment,
				Enabled: share.Enabled,
			})
		}
	}

	return result, nil
}

// CreateZvol mocks pool.dataset.create for ZVOLs.
func (m *MockClient) CreateZvol(ctx context.Context, params tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
	m.logCall("CreateZvol", params.Name, params.Volsize)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.datasets[params.Name]; exists {
		return nil, fmt.Errorf("ZVOL %s: %w", params.Name, ErrDatasetExists)
	}

	datasetID := fmt.Sprintf("zvol-%d", m.nextDatasetID)
	m.nextDatasetID++

	m.datasets[params.Name] = mockDataset{
		ID:      datasetID,
		Name:    params.Name,
		Type:    "VOLUME",
		Volsize: params.Volsize,
	}

	return &tnsapi.Dataset{
		ID:      datasetID,
		Name:    params.Name,
		Type:    "VOLUME",
		Volsize: map[string]interface{}{"parsed": float64(params.Volsize)},
	}, nil
}

// CreateNVMeOFSubsystem mocks nvmet.subsys.create.
func (m *MockClient) CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	m.logCall("CreateNVMeOFSubsystem", params.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	subsysID := m.nextSubsystemID
	m.nextSubsystemID++

	nqn := fmt.Sprintf("nqn.2014-08.org.nvmexpress:uuid:test-%d:%s", subsysID, params.Name)

	m.subsystems[params.Name] = mockSubsystem{
		ID:   subsysID,
		Name: params.Name,
		NQN:  nqn,
	}

	return &tnsapi.NVMeOFSubsystem{
		ID:   subsysID,
		Name: params.Name,
		NQN:  nqn,
	}, nil
}

// DeleteNVMeOFSubsystem mocks nvmet.subsys.delete.
func (m *MockClient) DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error {
	m.logCall("DeleteNVMeOFSubsystem", subsystemID)

	m.mu.Lock()
	defer m.mu.Unlock()

	for name, subsys := range m.subsystems {
		if subsys.ID == subsystemID {
			delete(m.subsystems, name)
			return nil
		}
	}

	return fmt.Errorf("subsystem %d: %w", subsystemID, ErrSubsystemNotFound)
}

// GetNVMeOFSubsystemByNQN mocks getting a subsystem by NQN.
func (m *MockClient) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	m.logCall("GetNVMeOFSubsystemByNQN", nqn)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, subsys := range m.subsystems {
		if subsys.Name == nqn || subsys.NQN == nqn {
			return &tnsapi.NVMeOFSubsystem{
				ID:   subsys.ID,
				Name: subsys.Name,
				NQN:  subsys.NQN,
			}, nil
		}
	}

	return nil, fmt.Errorf("subsystem %s: %w", nqn, ErrSubsystemNotFound)
}

// QueryNVMeOFSubsystem mocks querying subsystems.
func (m *MockClient) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error) {
	m.logCall("QueryNVMeOFSubsystem", nqn)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.NVMeOFSubsystem
	for _, subsys := range m.subsystems {
		if subsys.Name == nqn || subsys.NQN == nqn {
			result = append(result, tnsapi.NVMeOFSubsystem{
				ID:   subsys.ID,
				Name: subsys.Name,
				NQN:  subsys.NQN,
			})
		}
	}

	return result, nil
}

// ListAllNVMeOFSubsystems mocks listing all subsystems.
func (m *MockClient) ListAllNVMeOFSubsystems(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	m.logCall("ListAllNVMeOFSubsystems")

	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.NVMeOFSubsystem, 0, len(m.subsystems))
	for _, subsys := range m.subsystems {
		result = append(result, tnsapi.NVMeOFSubsystem{
			ID:   subsys.ID,
			Name: subsys.Name,
			NQN:  subsys.NQN,
		})
	}

	return result, nil
}

// CreateNVMeOFNamespace mocks nvmet.namespace.create.
func (m *MockClient) CreateNVMeOFNamespace(ctx context.Context, params tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
	m.logCall("CreateNVMeOFNamespace", params.DevicePath, params.SubsysID)

	m.mu.Lock()
	defer m.mu.Unlock()

	nsID := m.nextNamespaceID
	m.nextNamespaceID++

	m.namespaces[nsID] = mockNamespace{
		ID:          nsID,
		Device:      params.DevicePath,
		SubsystemID: params.SubsysID,
		NSID:        nsID,
	}

	return &tnsapi.NVMeOFNamespace{
		ID:        nsID,
		Device:    params.DevicePath,
		Subsystem: params.SubsysID,
		NSID:      nsID,
	}, nil
}

// DeleteNVMeOFNamespace mocks nvmet.namespace.delete.
func (m *MockClient) DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error {
	m.logCall("DeleteNVMeOFNamespace", namespaceID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespaceID]; !exists {
		return fmt.Errorf("namespace %d: %w", namespaceID, ErrNVMeOFTargetNotFound)
	}

	delete(m.namespaces, namespaceID)
	return nil
}

// QueryAllNVMeOFNamespaces mocks nvmeof.namespace.query.
func (m *MockClient) QueryAllNVMeOFNamespaces(ctx context.Context) ([]tnsapi.NVMeOFNamespace, error) {
	m.logCall("QueryAllNVMeOFNamespaces")

	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.NVMeOFNamespace, 0, len(m.namespaces))
	for _, ns := range m.namespaces {
		result = append(result, tnsapi.NVMeOFNamespace{
			ID:        ns.ID,
			Device:    ns.Device,
			Subsystem: ns.SubsystemID,
			NSID:      ns.NSID,
		})
	}

	return result, nil
}

// AddSubsystemToPort mocks nvmet.port_subsys.create.
func (m *MockClient) AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error {
	m.logCall("AddSubsystemToPort", subsystemID, portID)
	// Mock implementation - just return success
	return nil
}

// QueryNVMeOFPorts mocks nvmet.port.query.
func (m *MockClient) QueryNVMeOFPorts(ctx context.Context) ([]tnsapi.NVMeOFPort, error) {
	m.logCall("QueryNVMeOFPorts")

	// Return a mock port
	return []tnsapi.NVMeOFPort{
		{
			ID:        1,
			Transport: "tcp",
			Address:   "0.0.0.0",
			Port:      4420,
		},
	}, nil
}

// CreateSnapshot mocks zfs.snapshot.create.
func (m *MockClient) CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	m.logCall("CreateSnapshot", params.Dataset, params.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	snapshotID := fmt.Sprintf("%s@%s", params.Dataset, params.Name)

	m.snapshots[snapshotID] = mockSnapshot{
		ID:      snapshotID,
		Name:    params.Name,
		Dataset: params.Dataset,
	}

	return &tnsapi.Snapshot{
		ID:      snapshotID,
		Name:    params.Name,
		Dataset: params.Dataset,
	}, nil
}

// DeleteSnapshot mocks zfs.snapshot.delete.
func (m *MockClient) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	m.logCall("DeleteSnapshot", snapshotID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.snapshots[snapshotID]; !exists {
		return fmt.Errorf("snapshot %s: %w", snapshotID, ErrSnapshotNotFound)
	}

	delete(m.snapshots, snapshotID)
	return nil
}

// QuerySnapshots mocks zfs.snapshot.query.
func (m *MockClient) QuerySnapshots(ctx context.Context, filters []any) ([]tnsapi.Snapshot, error) {
	m.logCall("QuerySnapshots", filters)

	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.Snapshot, 0, len(m.snapshots))
	for _, snap := range m.snapshots {
		// Apply filters if provided
		if !matchesSnapshotFilters(snap, filters) {
			continue
		}
		result = append(result, tnsapi.Snapshot{
			ID:      snap.ID,
			Name:    snap.Name,
			Dataset: snap.Dataset,
		})
	}

	return result, nil
}

// matchesSnapshotFilters checks if a snapshot matches the provided filters.
func matchesSnapshotFilters(snap mockSnapshot, filters []any) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filterAny := range filters {
		filter, ok := filterAny.([]any)
		if !ok || len(filter) < 3 {
			continue
		}

		field, _ := filter[0].(string)
		operator, _ := filter[1].(string)
		value := filter[2]

		switch field {
		case "id":
			if operator == "=" {
				if valueStr, ok := value.(string); ok && snap.ID != valueStr {
					return false
				}
			}
		case "name":
			if operator == "=" {
				if valueStr, ok := value.(string); ok && snap.Name != valueStr {
					return false
				}
			}
		case "dataset":
			if operator == "=" {
				if valueStr, ok := value.(string); ok && snap.Dataset != valueStr {
					return false
				}
			}
		}
	}

	return true
}

// CloneSnapshot mocks zfs.snapshot.clone.
func (m *MockClient) CloneSnapshot(ctx context.Context, params tnsapi.CloneSnapshotParams) (*tnsapi.Dataset, error) {
	m.logCall("CloneSnapshot", params.Snapshot, params.Dataset)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if snapshot exists
	if _, exists := m.snapshots[params.Snapshot]; !exists {
		return nil, fmt.Errorf("snapshot %s: %w", params.Snapshot, ErrSnapshotNotFound)
	}

	// Create cloned dataset
	datasetID := fmt.Sprintf("dataset-%d", m.nextDatasetID)
	m.nextDatasetID++

	m.datasets[params.Dataset] = mockDataset{
		ID:         datasetID,
		Name:       params.Dataset,
		Type:       "FILESYSTEM",
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.Dataset,
	}

	return &tnsapi.Dataset{
		ID:         datasetID,
		Name:       params.Dataset,
		Type:       "FILESYSTEM",
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.Dataset,
	}, nil
}

// GetCallLog returns the list of API calls made (for debugging).
func (m *MockClient) GetCallLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := make([]string, len(m.callLog))
	copy(log, m.callLog)
	return log
}

// Close is a no-op for the mock client.
func (m *MockClient) Close() {
	// No-op for mock
}

// Verify that MockClient implements ClientInterface at compile time.
var _ tnsapi.ClientInterface = (*MockClient)(nil)
