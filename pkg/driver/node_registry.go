package driver

import (
	"sync"
	"time"
)

// NodeRegistry tracks registered nodes for validation.
// This is used by ControllerPublishVolume to verify node existence.
type NodeRegistry struct {
	nodes map[string]time.Time
	mu    sync.RWMutex
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
