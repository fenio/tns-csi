// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"sync"
)

// CleanupFunc is a function that performs cleanup and returns an error if it fails.
type CleanupFunc func() error

// CleanupTracker tracks cleanup functions to be executed on teardown.
type CleanupTracker struct {
	cleanups []CleanupFunc
	mu       sync.Mutex
}

// NewCleanupTracker creates a new CleanupTracker.
func NewCleanupTracker() *CleanupTracker {
	return &CleanupTracker{
		cleanups: make([]CleanupFunc, 0),
	}
}

// Add registers a cleanup function to be called on teardown.
// Cleanup functions are executed in reverse order (LIFO).
func (c *CleanupTracker) Add(fn CleanupFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanups = append(c.cleanups, fn)
}

// RunAll executes all registered cleanup functions in reverse order.
// It continues executing cleanups even if some fail, collecting all errors.
func (c *CleanupTracker) RunAll() []error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errors []error

	// Execute in reverse order (LIFO)
	for i := len(c.cleanups) - 1; i >= 0; i-- {
		if err := c.cleanups[i](); err != nil {
			errors = append(errors, err)
		}
	}

	// Clear the cleanup list
	c.cleanups = make([]CleanupFunc, 0)

	return errors
}

// Count returns the number of registered cleanup functions.
func (c *CleanupTracker) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cleanups)
}
