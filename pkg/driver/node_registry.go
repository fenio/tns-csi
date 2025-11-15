package driver

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// NodeRegistry tracks registered nodes for validation.
// This is used by ControllerPublishVolume to verify node existence.
type NodeRegistry struct {
	nodes map[string]time.Time
	mu    sync.RWMutex
}

// NVMeOFNamespaceRegistry tracks active NVMe-oF namespaces per subsystem (NQN).
// This prevents premature disconnection of shared subsystems when multiple
// volumes (namespaces) are using the same NVMe-oF target.
//
// Key format: "nqn:nsid" (e.g., "nqn.2005-03.org.truenas:csi-test:2")
// Value: reference count for this namespace
type NVMeOFNamespaceRegistry struct {
	// namespaces tracks individual namespace usage
	namespaces map[string]int
	// nqnCounts tracks total namespace count per NQN for quick lookup
	nqnCounts map[string]int
	mu        sync.RWMutex
}

// NewNVMeOFNamespaceRegistry creates a new namespace registry.
func NewNVMeOFNamespaceRegistry() *NVMeOFNamespaceRegistry {
	return &NVMeOFNamespaceRegistry{
		namespaces: make(map[string]int),
		nqnCounts:  make(map[string]int),
	}
}

// Register adds or increments a namespace reference.
func (r *NVMeOFNamespaceRegistry) Register(nqn, nsid string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", nqn, nsid)
	r.namespaces[key]++
	r.nqnCounts[nqn]++

	klog.V(4).Infof("Registered NVMe-oF namespace: NQN=%s, NSID=%s, count=%d, total_for_nqn=%d",
		nqn, nsid, r.namespaces[key], r.nqnCounts[nqn])
}

// Unregister decrements or removes a namespace reference.
// Returns true if this was the last namespace for the given NQN.
func (r *NVMeOFNamespaceRegistry) Unregister(nqn, nsid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", nqn, nsid)
	if count, exists := r.namespaces[key]; exists {
		count--
		if count <= 0 {
			delete(r.namespaces, key)
			klog.V(4).Infof("Removed NVMe-oF namespace: NQN=%s, NSID=%s", nqn, nsid)
		} else {
			r.namespaces[key] = count
			klog.V(4).Infof("Decremented NVMe-oF namespace: NQN=%s, NSID=%s, count=%d", nqn, nsid, count)
		}
	}

	// Decrement NQN count
	if nqnCount, exists := r.nqnCounts[nqn]; exists {
		nqnCount--
		if nqnCount <= 0 {
			delete(r.nqnCounts, nqn)
			klog.Infof("Last namespace for NQN=%s unregistered, safe to disconnect", nqn)
			return true
		}
		r.nqnCounts[nqn] = nqnCount
		klog.Infof("NQN=%s still has %d active namespace(s), skipping disconnect", nqn, nqnCount)
	}

	return false
}

// GetNQNCount returns the number of active namespaces for a given NQN.
func (r *NVMeOFNamespaceRegistry) GetNQNCount(nqn string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.nqnCounts[nqn]
}

// GetNamespaceCount returns the reference count for a specific namespace.
func (r *NVMeOFNamespaceRegistry) GetNamespaceCount(nqn, nsid string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := fmt.Sprintf("%s:%s", nqn, nsid)
	return r.namespaces[key]
}

// NewNodeRegistry creates a new node registry.
func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		nodes: make(map[string]time.Time),
	}
}

// Register adds a node to the registry.
func (r *NodeRegistry) Register(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[nodeID] = time.Now()
}

// IsRegistered checks if a node is registered.
func (r *NodeRegistry) IsRegistered(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.nodes[nodeID]
	return exists
}

// Unregister removes a node from the registry.
func (r *NodeRegistry) Unregister(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, nodeID)
}

// Count returns the number of registered nodes.
func (r *NodeRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}
