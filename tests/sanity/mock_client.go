// Package sanity provides mock implementations for CSI sanity testing.
package sanity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
	// ErrISCSITargetNotFound indicates an iSCSI target was not found.
	ErrISCSITargetNotFound = errors.New("iSCSI target not found")
	// ErrISCSIExtentNotFound indicates an iSCSI extent was not found.
	ErrISCSIExtentNotFound = errors.New("iSCSI extent not found")
	// ErrISCSITargetExtentNotFound indicates an iSCSI target-extent was not found.
	ErrISCSITargetExtentNotFound = errors.New("iSCSI target-extent not found")
)

// MockClient is a mock implementation of the TrueNAS API client for sanity testing.
type MockClient struct {
	datasets           map[string]mockDataset
	nfsShares          map[int]mockNFSShare
	nvmeofTargets      map[int]mockNVMeOFTarget
	snapshots          map[string]mockSnapshot
	subsystems         map[string]mockSubsystem
	namespaces         map[int]mockNamespace
	iscsiTargets       map[int]mockISCSITarget
	iscsiExtents       map[int]mockISCSIExtent
	iscsiTargetExtents map[int]mockISCSITargetExtent
	callLog            []string
	nextDatasetID      int
	nextShareID        int
	nextTargetID       int
	nextSnapshotID     int
	nextSubsystemID    int
	nextNamespaceID    int
	nextISCSITargetID  int
	nextISCSIExtentID  int
	nextISCSITEID      int // target-extent ID
	mu                 sync.Mutex
}

//nolint:govet // fieldalignment: field order prioritizes readability.
type mockDataset struct {
	ID             string
	Name           string
	Type           string
	Used           map[string]any
	Available      map[string]any
	Mountpoint     string
	UserProperties map[string]string // ZFS user properties for CSI metadata
	Volsize        int64
	Capacity       int64 // Store requested capacity for CSI volume
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

//nolint:govet // fieldalignment: keeping fields in logical order for readability
type mockSnapshot struct {
	ID         string
	Name       string
	Dataset    string
	Properties map[string]interface{}
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

type mockISCSITarget struct {
	Name   string
	Alias  string
	Mode   string
	Groups []tnsapi.ISCSITargetGroup
	ID     int
}

type mockISCSIExtent struct {
	Name      string
	Type      string
	Disk      string
	Path      string
	RPM       string
	Comment   string
	ID        int
	Blocksize int
	Enabled   bool
}

type mockISCSITargetExtent struct {
	ID     int
	Target int
	Extent int
	LunID  int
}

// NewMockClient creates a new mock TrueNAS API client.
func NewMockClient() *MockClient {
	return &MockClient{
		datasets:           make(map[string]mockDataset),
		nfsShares:          make(map[int]mockNFSShare),
		nvmeofTargets:      make(map[int]mockNVMeOFTarget),
		snapshots:          make(map[string]mockSnapshot),
		subsystems:         make(map[string]mockSubsystem),
		namespaces:         make(map[int]mockNamespace),
		iscsiTargets:       make(map[int]mockISCSITarget),
		iscsiExtents:       make(map[int]mockISCSIExtent),
		iscsiTargetExtents: make(map[int]mockISCSITargetExtent),
		nextDatasetID:      1,
		nextShareID:        1,
		nextTargetID:       1,
		nextSnapshotID:     1,
		nextSubsystemID:    1,
		nextNamespaceID:    1,
		nextISCSITargetID:  1,
		nextISCSIExtentID:  1,
		nextISCSITEID:      1,
		callLog:            make([]string, 0),
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

	// Use the dataset name as the ID, matching real TrueNAS API behavior
	// where Dataset.ID is the full ZFS path (e.g., "tank/parent/pvc-xxx")
	m.nextDatasetID++

	m.datasets[params.Name] = mockDataset{
		ID:         params.Name,
		Name:       params.Name,
		Type:       params.Type,
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)}, // 100GB
		Mountpoint: "/mnt/" + params.Name,
	}

	return &tnsapi.Dataset{
		ID:         params.Name,
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

// Dataset mocks pool.dataset.query.
func (m *MockClient) Dataset(ctx context.Context, name string) (*tnsapi.Dataset, error) {
	m.logCall("Dataset", name)

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
// The prefix parameter is used for filtering:
// - If empty, returns all datasets.
// - If starts with "/mnt/", matches datasets by mountpoint.
// - Otherwise, does prefix matching on dataset name.
func (m *MockClient) QueryAllDatasets(ctx context.Context, prefix string) ([]tnsapi.Dataset, error) {
	m.logCall("QueryAllDatasets", prefix)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.Dataset
	for _, ds := range m.datasets {
		var match bool
		switch {
		case prefix == "":
			match = true
		case len(prefix) > 4 && prefix[:5] == "/mnt/":
			// Match by mountpoint
			match = ds.Mountpoint == prefix
		default:
			// Prefix matching on dataset name
			match = len(ds.Name) >= len(prefix) && ds.Name[:len(prefix)] == prefix
		}
		if match {
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

// SetDatasetProperties mocks pool.dataset.update with user_properties.
func (m *MockClient) SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error {
	m.logCall("SetDatasetProperties", datasetID, properties)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for name, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			if ds.UserProperties == nil {
				ds.UserProperties = make(map[string]string)
			}
			for k, v := range properties {
				ds.UserProperties[k] = v
			}
			m.datasets[name] = ds
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// SetSnapshotProperties mocks pool.dataset.update with user_properties.
func (m *MockClient) SetSnapshotProperties(ctx context.Context, snapshotID string, updateProperties map[string]string, removeProperties []string) error {
	m.logCall("SetSnapshotProperties", snapshotID, updateProperties, removeProperties)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find snapshot by ID or name
	for name, snap := range m.snapshots {
		if snap.ID != snapshotID && snap.Name != snapshotID {
			continue
		}
		if snap.Properties == nil {
			snap.Properties = make(map[string]interface{})
		}
		for k, v := range updateProperties {
			snap.Properties[k] = v
		}
		for _, k := range removeProperties {
			delete(snap.Properties, k)
		}
		m.snapshots[name] = snap
		return nil
	}

	return fmt.Errorf("snapshot %s: %w", snapshotID, ErrSnapshotNotFound)
}

// GetDatasetProperties mocks pool.dataset.query with extra properties.
func (m *MockClient) GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error) {
	m.logCall("GetDatasetProperties", datasetID, propertyNames)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for _, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			result := make(map[string]string)
			if ds.UserProperties != nil {
				for _, name := range propertyNames {
					if val, ok := ds.UserProperties[name]; ok {
						result[name] = val
					}
				}
			}
			return result, nil
		}
	}

	return nil, fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// GetDatasetWithProperties queries a single dataset by exact ID with all user properties.
func (m *MockClient) GetDatasetWithProperties(ctx context.Context, datasetID string) (*tnsapi.DatasetWithProperties, error) {
	m.logCall("GetDatasetWithProperties", datasetID)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			userProps := make(map[string]tnsapi.UserProperty)
			for k, v := range ds.UserProperties {
				userProps[k] = tnsapi.UserProperty{Value: v}
			}
			result := &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{
					ID:         ds.ID,
					Name:       ds.Name,
					Type:       ds.Type,
					Used:       ds.Used,
					Available:  ds.Available,
					Mountpoint: ds.Mountpoint,
					Volsize:    map[string]interface{}{"parsed": float64(ds.Volsize)},
				},
				UserProperties: userProps,
			}
			return result, nil
		}
	}

	return nil, nil //nolint:nilnil // Not found
}

// GetAllDatasetProperties mocks pool.dataset.query to get all user properties.
func (m *MockClient) GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error) {
	m.logCall("GetAllDatasetProperties", datasetID)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for _, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			result := make(map[string]string)
			if ds.UserProperties != nil {
				for k, v := range ds.UserProperties {
					result[k] = v
				}
			}
			return result, nil
		}
	}

	return nil, fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// InheritDatasetProperty mocks pool.dataset.inherit.
func (m *MockClient) InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error {
	m.logCall("InheritDatasetProperty", datasetID, propertyName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for name, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			if ds.UserProperties != nil {
				delete(ds.UserProperties, propertyName)
				m.datasets[name] = ds
			}
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// ClearDatasetProperties mocks clearing multiple ZFS user properties.
func (m *MockClient) ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error {
	m.logCall("ClearDatasetProperties", datasetID, propertyNames)

	for _, name := range propertyNames {
		if err := m.InheritDatasetProperty(ctx, datasetID, name); err != nil {
			return err
		}
	}

	return nil
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
//
//nolint:dupl // Similar loop-and-filter pattern is acceptable in test mocks.
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
// The pathPrefix parameter is used for filtering:
// - If empty, returns all shares.
// - If starts with "/", does prefix matching on the path.
// - Otherwise, checks if the path ends with "/" + pathPrefix (for volume name lookup).
func (m *MockClient) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]tnsapi.NFSShare, error) {
	m.logCall("QueryAllNFSShares", pathPrefix)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.NFSShare
	for _, share := range m.nfsShares {
		var match bool
		switch {
		case pathPrefix == "":
			match = true
		case pathPrefix[0] == '/':
			// Prefix matching for absolute paths
			match = len(share.Path) >= len(pathPrefix) && share.Path[:len(pathPrefix)] == pathPrefix
		default:
			// Suffix matching for volume name lookup (path ends with /volumeName)
			suffix := "/" + pathPrefix
			match = len(share.Path) >= len(suffix) && share.Path[len(share.Path)-len(suffix):] == suffix
		}
		if match {
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

	// Use the ZVOL name as the ID, matching real TrueNAS API behavior
	// where Dataset.ID is the full ZFS path (e.g., "tank/parent/pvc-xxx")
	m.nextDatasetID++

	m.datasets[params.Name] = mockDataset{
		ID:      params.Name,
		Name:    params.Name,
		Type:    "VOLUME",
		Volsize: params.Volsize,
	}

	return &tnsapi.Dataset{
		ID:      params.Name,
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

// NVMeOFSubsystemByNQN mocks getting a subsystem by NQN.
func (m *MockClient) NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	m.logCall("NVMeOFSubsystemByNQN", nqn)

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
		ID:     nsID,
		Device: params.DevicePath,
		Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: params.SubsysID},
		NSID:   nsID,
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
			ID:     ns.ID,
			Device: ns.Device,
			Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: ns.SubsystemID},
			NSID:   ns.NSID,
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

// RemoveSubsystemFromPort mocks nvmet.port_subsys.delete.
func (m *MockClient) RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error {
	m.logCall("RemoveSubsystemFromPort", portSubsysID)
	// Mock implementation - in reality would remove port-subsystem binding
	return nil
}

// QuerySubsystemPortBindings mocks nvmet.port_subsys.query for a specific subsystem.
func (m *MockClient) QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error) {
	m.logCall("QuerySubsystemPortBindings", subsystemID)
	// Mock implementation - return empty list (no bindings to clean up in mock)
	return []tnsapi.NVMeOFPortSubsystem{}, nil
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

// QuerySnapshotIDs mocks zfs.snapshot.query with select: ["id"].
// Returns only snapshot IDs to minimize response size.
func (m *MockClient) QuerySnapshotIDs(ctx context.Context, filters []any) ([]string, error) {
	m.logCall("QuerySnapshotIDs", filters)

	m.mu.Lock()
	defer m.mu.Unlock()

	var ids []string
	for _, snap := range m.snapshots {
		if !matchesSnapshotFilters(snap, filters) {
			continue
		}
		ids = append(ids, snap.ID)
	}

	return ids, nil
}

// matchesSnapshotFilters checks if a snapshot matches the provided filters.
//
//nolint:goconst // Filter field names are used locally in mock filter functions.
func matchesSnapshotFilters(snap mockSnapshot, filters []any) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filterAny := range filters {
		filter, ok := filterAny.([]any)
		if !ok || len(filter) < 3 {
			continue
		}

		field, ok := filter[0].(string)
		if !ok {
			continue
		}
		operator, ok := filter[1].(string)
		if !ok {
			continue
		}
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

	// Create cloned dataset - use name as ID, matching real TrueNAS API behavior
	m.nextDatasetID++

	m.datasets[params.Dataset] = mockDataset{
		ID:         params.Dataset,
		Name:       params.Dataset,
		Type:       "FILESYSTEM",
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.Dataset,
	}

	return &tnsapi.Dataset{
		ID:         params.Dataset,
		Name:       params.Dataset,
		Type:       "FILESYSTEM",
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.Dataset,
	}, nil
}

// PromoteDataset mocks pool.dataset.promote.
// This simulates promoting a cloned dataset to become independent from its origin.
func (m *MockClient) PromoteDataset(ctx context.Context, datasetID string) error {
	m.logCall("PromoteDataset", datasetID)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID or name
	for _, ds := range m.datasets {
		if ds.ID == datasetID || ds.Name == datasetID {
			// In a real implementation, this would break the clone-parent relationship
			// For the mock, we just verify the dataset exists
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", datasetID, ErrDatasetNotFound)
}

// RunOnetimeReplication mocks replication.run_onetime.
// This simulates a one-time zfs send/receive operation for detached snapshots.
func (m *MockClient) RunOnetimeReplication(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams) (int, error) {
	m.logCall("RunOnetimeReplication", params.SourceDatasets, params.TargetDataset)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate source datasets exist
	for _, srcDataset := range params.SourceDatasets {
		found := false
		for _, ds := range m.datasets {
			if ds.Name == srcDataset {
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("source dataset %s: %w", srcDataset, ErrDatasetNotFound)
		}
	}

	// Create the target dataset as a copy
	datasetID := fmt.Sprintf("dataset-%d", m.nextDatasetID)
	m.nextDatasetID++

	m.datasets[params.TargetDataset] = mockDataset{
		ID:         datasetID,
		Name:       params.TargetDataset,
		Type:       "FILESYSTEM",
		Used:       map[string]any{"parsed": float64(0)},
		Available:  map[string]any{"parsed": float64(107374182400)},
		Mountpoint: "/mnt/" + params.TargetDataset,
	}

	// Return a mock job ID
	return 12345, nil
}

// GetJobStatus mocks core.get_jobs to get job status.
func (m *MockClient) GetJobStatus(ctx context.Context, jobID int) (*tnsapi.ReplicationJobState, error) {
	m.logCall("GetJobStatus", jobID)

	// Return a completed job status for the mock
	return &tnsapi.ReplicationJobState{
		ID:       jobID,
		State:    "SUCCESS",
		Progress: map[string]interface{}{"percent": float64(100)},
	}, nil
}

// WaitForJob mocks waiting for a job to complete.
func (m *MockClient) WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error {
	m.logCall("WaitForJob", jobID, pollInterval)

	// In mock, jobs complete immediately
	return nil
}

// RunOnetimeReplicationAndWait mocks running replication and waiting for completion.
func (m *MockClient) RunOnetimeReplicationAndWait(ctx context.Context, params tnsapi.ReplicationRunOnetimeParams, pollInterval time.Duration) error {
	m.logCall("RunOnetimeReplicationAndWait", params.SourceDatasets, params.TargetDataset)

	// Run the replication
	_, err := m.RunOnetimeReplication(ctx, params)
	if err != nil {
		return err
	}

	// In mock, it completes immediately
	return nil
}

// FindDatasetsByProperty searches for datasets that have a specific ZFS user property value.
func (m *MockClient) FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]tnsapi.DatasetWithProperties, error) {
	m.logCall("FindDatasetsByProperty", prefix, propertyName, propertyValue)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.DatasetWithProperties
	for _, ds := range m.datasets {
		// Check if dataset name starts with prefix
		if len(ds.Name) < len(prefix) || ds.Name[:len(prefix)] != prefix {
			continue
		}

		// Check if dataset has the matching property
		if ds.UserProperties != nil {
			if val, ok := ds.UserProperties[propertyName]; ok && val == propertyValue {
				// Convert UserProperties to tnsapi.UserProperty format
				userProps := make(map[string]tnsapi.UserProperty)
				for k, v := range ds.UserProperties {
					userProps[k] = tnsapi.UserProperty{Value: v}
				}
				result = append(result, tnsapi.DatasetWithProperties{
					Dataset: tnsapi.Dataset{
						ID:         ds.ID,
						Name:       ds.Name,
						Type:       ds.Type,
						Used:       ds.Used,
						Available:  ds.Available,
						Mountpoint: ds.Mountpoint,
						Volsize:    map[string]interface{}{"parsed": float64(ds.Volsize)},
					},
					UserProperties: userProps,
				})
			}
		}
	}

	return result, nil
}

// FindManagedDatasets finds all datasets managed by tns-csi under the given prefix.
func (m *MockClient) FindManagedDatasets(ctx context.Context, prefix string) ([]tnsapi.DatasetWithProperties, error) {
	return m.FindDatasetsByProperty(ctx, prefix, tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
}

// FindDatasetByCSIVolumeName finds a dataset by its CSI volume name.
func (m *MockClient) FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*tnsapi.DatasetWithProperties, error) {
	datasets, err := m.FindDatasetsByProperty(ctx, prefix, tnsapi.PropertyCSIVolumeName, csiVolumeName)
	if err != nil {
		return nil, err
	}

	if len(datasets) == 0 {
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil dataset
	}

	return &datasets[0], nil
}

// GetCallLog returns the list of API calls made (for debugging).
func (m *MockClient) GetCallLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := make([]string, len(m.callLog))
	copy(log, m.callLog)
	return log
}

// =============================================================================
// iSCSI operations
// =============================================================================

// GetISCSIGlobalConfig returns the global iSCSI configuration.
func (m *MockClient) GetISCSIGlobalConfig(ctx context.Context) (*tnsapi.ISCSIGlobalConfig, error) {
	m.logCall("GetISCSIGlobalConfig")

	return &tnsapi.ISCSIGlobalConfig{
		ID:                 1,
		Basename:           "iqn.2005-10.org.freenas.ctl",
		ISNSServers:        []string{},
		PoolAvailThreshold: nil,
	}, nil
}

// QueryISCSIPortals returns all iSCSI portals.
func (m *MockClient) QueryISCSIPortals(ctx context.Context) ([]tnsapi.ISCSIPortal, error) {
	m.logCall("QueryISCSIPortals")

	return []tnsapi.ISCSIPortal{
		{
			ID:      1,
			Tag:     1,
			Comment: "Default portal",
			Listen: []tnsapi.ISCSIPortalListen{
				{IP: "0.0.0.0", Port: 3260},
			},
		},
	}, nil
}

// QueryISCSIInitiators returns all iSCSI initiator groups.
func (m *MockClient) QueryISCSIInitiators(ctx context.Context) ([]tnsapi.ISCSIInitiator, error) {
	m.logCall("QueryISCSIInitiators")

	return []tnsapi.ISCSIInitiator{
		{
			ID:         1,
			Tag:        1,
			Comment:    "Allow all initiators",
			Initiators: []string{},
		},
	}, nil
}

// CreateISCSITarget creates a new iSCSI target.
func (m *MockClient) CreateISCSITarget(ctx context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
	m.logCall("CreateISCSITarget", params.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	targetID := m.nextISCSITargetID
	m.nextISCSITargetID++

	mode := params.Mode
	if mode == "" {
		mode = "ISCSI"
	}

	m.iscsiTargets[targetID] = mockISCSITarget{
		ID:     targetID,
		Name:   params.Name,
		Alias:  params.Alias,
		Mode:   mode,
		Groups: params.Groups,
	}

	return &tnsapi.ISCSITarget{
		ID:     targetID,
		Name:   params.Name,
		Alias:  params.Alias,
		Mode:   mode,
		Groups: params.Groups,
	}, nil
}

// DeleteISCSITarget deletes an iSCSI target.
func (m *MockClient) DeleteISCSITarget(ctx context.Context, targetID int, force bool) error {
	m.logCall("DeleteISCSITarget", targetID, force)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.iscsiTargets[targetID]; !exists {
		return fmt.Errorf("iSCSI target %d: %w", targetID, ErrISCSITargetNotFound)
	}

	delete(m.iscsiTargets, targetID)
	return nil
}

// QueryISCSITargets queries iSCSI targets.
func (m *MockClient) QueryISCSITargets(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITarget, error) {
	m.logCall("QueryISCSITargets", filters)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.ISCSITarget
	for _, target := range m.iscsiTargets {
		if matchesISCSITargetFilters(target, filters) {
			result = append(result, tnsapi.ISCSITarget{
				ID:     target.ID,
				Name:   target.Name,
				Alias:  target.Alias,
				Mode:   target.Mode,
				Groups: target.Groups,
			})
		}
	}

	return result, nil
}

// matchesISCSITargetFilters checks if a target matches the provided filters.
//
//nolint:dupl // Similar filter logic for different types is acceptable in test mocks.
func matchesISCSITargetFilters(target mockISCSITarget, filters []interface{}) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filterAny := range filters {
		filter, ok := filterAny.([]interface{})
		if !ok || len(filter) < 3 {
			continue
		}

		field, ok := filter[0].(string)
		if !ok {
			continue
		}
		operator, ok := filter[1].(string)
		if !ok {
			continue
		}
		value := filter[2]

		switch field {
		case "name":
			if operator == "=" {
				if valueStr, ok := value.(string); ok && target.Name != valueStr {
					return false
				}
			}
		case "id":
			if operator == "=" {
				if valueInt, ok := value.(int); ok && target.ID != valueInt {
					return false
				}
			}
		}
	}

	return true
}

// ISCSITargetByName finds an iSCSI target by name.
func (m *MockClient) ISCSITargetByName(ctx context.Context, name string) (*tnsapi.ISCSITarget, error) {
	m.logCall("ISCSITargetByName", name)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, target := range m.iscsiTargets {
		if target.Name == name {
			return &tnsapi.ISCSITarget{
				ID:     target.ID,
				Name:   target.Name,
				Alias:  target.Alias,
				Mode:   target.Mode,
				Groups: target.Groups,
			}, nil
		}
	}

	return nil, fmt.Errorf("iSCSI target %s: %w", name, ErrISCSITargetNotFound)
}

// CreateISCSIExtent creates a new iSCSI extent.
func (m *MockClient) CreateISCSIExtent(ctx context.Context, params tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
	m.logCall("CreateISCSIExtent", params.Name, params.Disk)

	m.mu.Lock()
	defer m.mu.Unlock()

	extentID := m.nextISCSIExtentID
	m.nextISCSIExtentID++

	enabled := true
	if params.Enabled != nil {
		enabled = *params.Enabled
	}

	blocksize := params.Blocksize
	if blocksize == 0 {
		blocksize = 512
	}

	m.iscsiExtents[extentID] = mockISCSIExtent{
		ID:        extentID,
		Name:      params.Name,
		Type:      params.Type,
		Disk:      params.Disk,
		Path:      params.Path,
		RPM:       params.RPM,
		Comment:   params.Comment,
		Blocksize: blocksize,
		Enabled:   enabled,
	}

	return &tnsapi.ISCSIExtent{
		ID:        extentID,
		Name:      params.Name,
		Type:      params.Type,
		Disk:      params.Disk,
		Path:      params.Path,
		RPM:       params.RPM,
		Comment:   params.Comment,
		Blocksize: blocksize,
		Enabled:   enabled,
	}, nil
}

// DeleteISCSIExtent deletes an iSCSI extent.
func (m *MockClient) DeleteISCSIExtent(ctx context.Context, extentID int, remove, force bool) error {
	m.logCall("DeleteISCSIExtent", extentID, remove, force)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.iscsiExtents[extentID]; !exists {
		return fmt.Errorf("iSCSI extent %d: %w", extentID, ErrISCSIExtentNotFound)
	}

	delete(m.iscsiExtents, extentID)
	return nil
}

// QueryISCSIExtents queries iSCSI extents.
func (m *MockClient) QueryISCSIExtents(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSIExtent, error) {
	m.logCall("QueryISCSIExtents", filters)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.ISCSIExtent
	for _, extent := range m.iscsiExtents {
		if matchesISCSIExtentFilters(extent, filters) {
			result = append(result, tnsapi.ISCSIExtent{
				ID:        extent.ID,
				Name:      extent.Name,
				Type:      extent.Type,
				Disk:      extent.Disk,
				Path:      extent.Path,
				RPM:       extent.RPM,
				Comment:   extent.Comment,
				Blocksize: extent.Blocksize,
				Enabled:   extent.Enabled,
			})
		}
	}

	return result, nil
}

// matchesISCSIExtentFilters checks if an extent matches the provided filters.
//
//nolint:dupl // Similar filter logic for different types is acceptable in test mocks.
func matchesISCSIExtentFilters(extent mockISCSIExtent, filters []interface{}) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filterAny := range filters {
		filter, ok := filterAny.([]interface{})
		if !ok || len(filter) < 3 {
			continue
		}

		field, ok := filter[0].(string)
		if !ok {
			continue
		}
		operator, ok := filter[1].(string)
		if !ok {
			continue
		}
		value := filter[2]

		switch field {
		case "name":
			if operator == "=" {
				if valueStr, ok := value.(string); ok && extent.Name != valueStr {
					return false
				}
			}
		case "id":
			if operator == "=" {
				if valueInt, ok := value.(int); ok && extent.ID != valueInt {
					return false
				}
			}
		}
	}

	return true
}

// ISCSIExtentByName finds an iSCSI extent by name.
func (m *MockClient) ISCSIExtentByName(ctx context.Context, name string) (*tnsapi.ISCSIExtent, error) {
	m.logCall("ISCSIExtentByName", name)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, extent := range m.iscsiExtents {
		if extent.Name == name {
			return &tnsapi.ISCSIExtent{
				ID:        extent.ID,
				Name:      extent.Name,
				Type:      extent.Type,
				Disk:      extent.Disk,
				Path:      extent.Path,
				RPM:       extent.RPM,
				Comment:   extent.Comment,
				Blocksize: extent.Blocksize,
				Enabled:   extent.Enabled,
			}, nil
		}
	}

	return nil, fmt.Errorf("iSCSI extent %s: %w", name, ErrISCSIExtentNotFound)
}

// CreateISCSITargetExtent creates a target-extent association.
func (m *MockClient) CreateISCSITargetExtent(ctx context.Context, params tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
	m.logCall("CreateISCSITargetExtent", params.Target, params.Extent, params.LunID)

	m.mu.Lock()
	defer m.mu.Unlock()

	teID := m.nextISCSITEID
	m.nextISCSITEID++

	m.iscsiTargetExtents[teID] = mockISCSITargetExtent{
		ID:     teID,
		Target: params.Target,
		Extent: params.Extent,
		LunID:  params.LunID,
	}

	return &tnsapi.ISCSITargetExtent{
		ID:     teID,
		Target: params.Target,
		Extent: params.Extent,
		LunID:  params.LunID,
	}, nil
}

// DeleteISCSITargetExtent deletes a target-extent association.
func (m *MockClient) DeleteISCSITargetExtent(ctx context.Context, teID int, force bool) error {
	m.logCall("DeleteISCSITargetExtent", teID, force)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.iscsiTargetExtents[teID]; !exists {
		return fmt.Errorf("iSCSI target-extent %d: %w", teID, ErrISCSITargetExtentNotFound)
	}

	delete(m.iscsiTargetExtents, teID)
	return nil
}

// QueryISCSITargetExtents queries target-extent associations.
func (m *MockClient) QueryISCSITargetExtents(ctx context.Context, filters []interface{}) ([]tnsapi.ISCSITargetExtent, error) {
	m.logCall("QueryISCSITargetExtents", filters)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.ISCSITargetExtent
	for _, te := range m.iscsiTargetExtents {
		if matchesISCSITargetExtentFilters(te, filters) {
			result = append(result, tnsapi.ISCSITargetExtent{
				ID:     te.ID,
				Target: te.Target,
				Extent: te.Extent,
				LunID:  te.LunID,
			})
		}
	}

	return result, nil
}

// matchesISCSITargetExtentFilters checks if a target-extent matches the provided filters.
func matchesISCSITargetExtentFilters(te mockISCSITargetExtent, filters []interface{}) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filterAny := range filters {
		filter, ok := filterAny.([]interface{})
		if !ok || len(filter) < 3 {
			continue
		}

		field, ok := filter[0].(string)
		if !ok {
			continue
		}
		operator, ok := filter[1].(string)
		if !ok {
			continue
		}
		value := filter[2]

		switch field {
		case "target":
			if operator == "=" {
				if valueInt, ok := value.(int); ok && te.Target != valueInt {
					return false
				}
			}
		case "extent":
			if operator == "=" {
				if valueInt, ok := value.(int); ok && te.Extent != valueInt {
					return false
				}
			}
		case "id":
			if operator == "=" {
				if valueInt, ok := value.(int); ok && te.ID != valueInt {
					return false
				}
			}
		}
	}

	return true
}

// ISCSITargetExtentByTarget finds target-extent associations for a target.
//
//nolint:dupl // Similar loop-and-filter pattern is acceptable in test mocks.
func (m *MockClient) ISCSITargetExtentByTarget(ctx context.Context, targetID int) ([]tnsapi.ISCSITargetExtent, error) {
	m.logCall("ISCSITargetExtentByTarget", targetID)

	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.ISCSITargetExtent
	for _, te := range m.iscsiTargetExtents {
		if te.Target == targetID {
			result = append(result, tnsapi.ISCSITargetExtent{
				ID:     te.ID,
				Target: te.Target,
				Extent: te.Extent,
				LunID:  te.LunID,
			})
		}
	}

	return result, nil
}

// ReloadISCSIService simulates reloading the iSCSI service.
func (m *MockClient) ReloadISCSIService(ctx context.Context) error {
	m.logCall("ReloadISCSIService")
	return nil // No-op for mock - always succeeds
}

// Close is a no-op for the mock client.
func (m *MockClient) Close() {
	// No-op for mock
}

// Verify that MockClient implements ClientInterface at compile time.
var _ tnsapi.ClientInterface = (*MockClient)(nil)
