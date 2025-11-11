package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// registryPath is the path to the formatted volumes registry file.
	registryPath = "/var/lib/tns-csi/formatted-volumes.json"
	// registryDir is the directory containing the registry file.
	registryDir = "/var/lib/tns-csi"
)

// FormattedVolumeEntry represents a record of a formatted volume.
type FormattedVolumeEntry struct {
	VolumeID       string    `json:"volume_id"`       // CSI volume ID
	DevicePath     string    `json:"device_path"`     // Device path at time of formatting
	FilesystemType string    `json:"filesystem_type"` // Filesystem type (ext4, xfs, etc.)
	FormattedAt    time.Time `json:"formatted_at"`    // Timestamp when formatted
}

// FormattedVolumesRegistry tracks which volumes have been formatted.
// This prevents accidental reformatting of existing volumes that contain user data.
type FormattedVolumesRegistry struct {
	mu      sync.RWMutex
	entries map[string]FormattedVolumeEntry // key: volume ID
}

// globalFormattedVolumesRegistry is the singleton registry instance.
var (
	globalFormattedVolumesRegistry     *FormattedVolumesRegistry
	globalFormattedVolumesRegistryOnce sync.Once
)

// getFormattedVolumesRegistry returns the singleton formatted volumes registry.
func getFormattedVolumesRegistry() *FormattedVolumesRegistry {
	globalFormattedVolumesRegistryOnce.Do(func() {
		globalFormattedVolumesRegistry = &FormattedVolumesRegistry{
			entries: make(map[string]FormattedVolumeEntry),
		}
		if err := globalFormattedVolumesRegistry.load(); err != nil {
			klog.Warningf("Failed to load formatted volumes registry (will start fresh): %v", err)
		}
	})
	return globalFormattedVolumesRegistry
}

// load reads the registry from disk.
func (r *FormattedVolumesRegistry) load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(registryDir, 0o750); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}

	// Read registry file
	data, err := os.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("Formatted volumes registry does not exist yet, starting with empty registry")
			return nil
		}
		return fmt.Errorf("failed to read registry file: %w", err)
	}

	// Parse JSON
	var entries []FormattedVolumeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("failed to parse registry file: %w", err)
	}

	// Populate map
	r.entries = make(map[string]FormattedVolumeEntry)
	for _, entry := range entries {
		r.entries[entry.VolumeID] = entry
	}

	klog.Infof("Loaded %d formatted volume entries from registry", len(r.entries))
	return nil
}

// save writes the registry to disk.
func (r *FormattedVolumesRegistry) save() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Convert map to slice
	entries := make([]FormattedVolumeEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(registryDir, 0o750); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}

	// Write to temporary file first (atomic write)
	tmpPath := registryPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o640); err != nil {
		return fmt.Errorf("failed to write temporary registry file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, registryPath); err != nil {
		return fmt.Errorf("failed to rename temporary registry file: %w", err)
	}

	klog.V(4).Infof("Saved %d formatted volume entries to registry", len(entries))
	return nil
}

// RecordFormatted records that a volume has been formatted.
func (r *FormattedVolumesRegistry) RecordFormatted(volumeID, devicePath, fsType string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := FormattedVolumeEntry{
		VolumeID:       volumeID,
		DevicePath:     devicePath,
		FilesystemType: fsType,
		FormattedAt:    time.Now(),
	}

	r.entries[volumeID] = entry
	klog.Infof("Recorded formatted volume: %s (device: %s, fs: %s)", volumeID, devicePath, fsType)

	// Save immediately
	r.mu.Unlock()
	if err := r.save(); err != nil {
		klog.Errorf("Failed to save formatted volumes registry: %v", err)
		r.mu.Lock()
		return err
	}
	r.mu.Lock()

	return nil
}

// WasFormatted checks if a volume was previously formatted.
func (r *FormattedVolumesRegistry) WasFormatted(volumeID string) (bool, *FormattedVolumeEntry) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if entry, exists := r.entries[volumeID]; exists {
		return true, &entry
	}
	return false, nil
}

// Remove removes a volume from the registry (e.g., when volume is deleted).
func (r *FormattedVolumesRegistry) Remove(volumeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[volumeID]; !exists {
		return nil // Already removed
	}

	delete(r.entries, volumeID)
	klog.V(4).Infof("Removed formatted volume from registry: %s", volumeID)

	// Save immediately
	r.mu.Unlock()
	if err := r.save(); err != nil {
		klog.Errorf("Failed to save formatted volumes registry after removal: %v", err)
		r.mu.Lock()
		return err
	}
	r.mu.Lock()

	return nil
}
