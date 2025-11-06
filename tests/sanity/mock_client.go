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
)

// MockClient is a mock implementation of the TrueNAS API client for sanity testing.
type MockClient struct {
	datasets      map[string]mockDataset
	nfsShares     map[int]mockNFSShare
	nvmeofTargets map[int]mockNVMeOFTarget
	callLog       []string
	nextDatasetID int
	nextShareID   int
	nextTargetID  int
	mu            sync.Mutex
}

type mockDataset struct {
	ID         string
	Name       string
	Type       string
	Used       map[string]interface{}
	Available  map[string]interface{}
	Mountpoint string
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

// NewMockClient creates a new mock TrueNAS API client.
func NewMockClient() *MockClient {
	return &MockClient{
		datasets:      make(map[string]mockDataset),
		nfsShares:     make(map[int]mockNFSShare),
		nvmeofTargets: make(map[int]mockNVMeOFTarget),
		nextDatasetID: 1,
		nextShareID:   1,
		nextTargetID:  1,
		callLog:       make([]string, 0),
	}
}

// logCall records an API call for debugging.
func (m *MockClient) logCall(method string, params ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callLog = append(m.callLog, fmt.Sprintf("%s(%v)", method, params))
}

// CreateDataset mocks pool.dataset.create.
func (m *MockClient) CreateDataset(name string, params map[string]interface{}) (string, error) {
	m.logCall("CreateDataset", name, params)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.datasets[name]; exists {
		return "", fmt.Errorf("dataset %s: %w", name, ErrDatasetExists)
	}

	datasetType := "FILESYSTEM"
	if t, ok := params["type"].(string); ok {
		datasetType = t
	}

	datasetID := fmt.Sprintf("dataset-%d", m.nextDatasetID)
	m.nextDatasetID++

	m.datasets[name] = mockDataset{
		ID:         datasetID,
		Name:       name,
		Type:       datasetType,
		Used:       map[string]interface{}{"parsed": float64(0)},
		Available:  map[string]interface{}{"parsed": float64(107374182400)}, // 100GB
		Mountpoint: "/mnt/" + name,
	}

	return datasetID, nil
}

// DeleteDataset mocks pool.dataset.delete.
func (m *MockClient) DeleteDataset(id string) error {
	m.logCall("DeleteDataset", id)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID
	for name, ds := range m.datasets {
		if ds.ID == id {
			delete(m.datasets, name)
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", id, ErrDatasetNotFound)
}

// GetDataset mocks pool.dataset.query.
func (m *MockClient) GetDataset(name string) (*tnsapi.Dataset, error) {
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
	}, nil
}

// CreateNFSShare mocks sharing.nfs.create.
func (m *MockClient) CreateNFSShare(path, comment string) (int, error) {
	m.logCall("CreateNFSShare", path, comment)

	m.mu.Lock()
	defer m.mu.Unlock()

	shareID := m.nextShareID
	m.nextShareID++

	m.nfsShares[shareID] = mockNFSShare{
		ID:      shareID,
		Path:    path,
		Comment: comment,
		Enabled: true,
	}

	return shareID, nil
}

// DeleteNFSShare mocks sharing.nfs.delete.
func (m *MockClient) DeleteNFSShare(id int) error {
	m.logCall("DeleteNFSShare", id)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.nfsShares[id]; !exists {
		return fmt.Errorf("NFS share %d: %w", id, ErrNFSShareNotFound)
	}

	delete(m.nfsShares, id)
	return nil
}

// CreateNVMeOFTarget mocks NVMe-oF target creation.
func (m *MockClient) CreateNVMeOFTarget(subsystemID int, devicePath string) (int, error) {
	m.logCall("CreateNVMeOFTarget", subsystemID, devicePath)

	m.mu.Lock()
	defer m.mu.Unlock()

	targetID := m.nextTargetID
	m.nextTargetID++

	m.nvmeofTargets[targetID] = mockNVMeOFTarget{
		ID:          targetID,
		SubsystemID: subsystemID,
		NamespaceID: targetID, // Simplified for mock
		NQN:         fmt.Sprintf("nqn.2014-08.org.nvmexpress:uuid:test-%d", targetID),
		DevicePath:  devicePath,
	}

	return targetID, nil
}

// DeleteNVMeOFTarget mocks NVMe-oF target deletion.
func (m *MockClient) DeleteNVMeOFTarget(namespaceID int) error {
	m.logCall("DeleteNVMeOFTarget", namespaceID)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and delete by namespace ID
	for id, target := range m.nvmeofTargets {
		if target.NamespaceID == namespaceID {
			delete(m.nvmeofTargets, id)
			return nil
		}
	}

	return fmt.Errorf("NVMe-oF target with namespace ID %d: %w", namespaceID, ErrNVMeOFTargetNotFound)
}

// SetDatasetQuota mocks pool.dataset.update quota setting.
func (m *MockClient) SetDatasetQuota(id string, quotaBytes int64) error {
	m.logCall("SetDatasetQuota", id, quotaBytes)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find dataset by ID
	for name, ds := range m.datasets {
		if ds.ID == id {
			// Update dataset (quota is implicit in mock)
			m.datasets[name] = ds
			return nil
		}
	}

	return fmt.Errorf("dataset %s: %w", id, ErrDatasetNotFound)
}

// QueryPool mocks pool.query for capacity information.
func (m *MockClient) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	m.logCall("QueryPool", poolName)

	m.mu.Lock()
	defer m.mu.Unlock()

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
