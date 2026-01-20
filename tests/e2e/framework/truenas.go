// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// ErrDatasetDeleteTimeout is returned when waiting for a dataset to be deleted times out.
var ErrDatasetDeleteTimeout = errors.New("timeout waiting for dataset to be deleted")

// ErrMissingIDField is returned when a TrueNAS resource is missing its ID field.
var ErrMissingIDField = errors.New("resource has no ID field")

// ErrInvalidIDType is returned when a TrueNAS resource ID cannot be converted to int.
var ErrInvalidIDType = errors.New("cannot convert resource ID to int")

// ErrDatasetNotFound is returned when a requested dataset doesn't exist.
var ErrDatasetNotFound = errors.New("dataset not found")

// TrueNASVerifier provides methods for verifying TrueNAS backend state.
type TrueNASVerifier struct {
	client *tnsapi.Client
}

// NewTrueNASVerifier creates a new TrueNASVerifier.
func NewTrueNASVerifier(host, apiKey string) (*TrueNASVerifier, error) {
	url := fmt.Sprintf("wss://%s/api/current", host)
	client, err := tnsapi.NewClient(url, apiKey, true) // skip TLS verify for tests
	if err != nil {
		return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	return &TrueNASVerifier{client: client}, nil
}

// Close closes the TrueNAS client connection.
func (v *TrueNASVerifier) Close() {
	if v.client != nil {
		v.client.Close()
	}
}

// DatasetExists checks if a dataset exists on TrueNAS.
func (v *TrueNASVerifier) DatasetExists(ctx context.Context, datasetPath string) (bool, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter}, &datasets); err != nil {
		return false, fmt.Errorf("failed to query dataset: %w", err)
	}
	return len(datasets) > 0, nil
}

// WaitForDatasetDeleted polls TrueNAS until the dataset is confirmed deleted or timeout.
func (v *TrueNASVerifier) WaitForDatasetDeleted(ctx context.Context, datasetPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		exists, err := v.DatasetExists(ctx, datasetPath)
		if err != nil {
			// Log but continue polling - transient errors are possible
			fmt.Printf("Warning: error checking dataset existence: %v\n", err)
		} else if !exists {
			return nil // Dataset is deleted
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
			// Continue polling
		}
	}

	return fmt.Errorf("%w: %s", ErrDatasetDeleteTimeout, datasetPath)
}

// NFSShareExists checks if an NFS share exists for the given path.
func (v *TrueNASVerifier) NFSShareExists(ctx context.Context, path string) (bool, error) {
	var shares []map[string]any
	filter := []any{[]any{"path", "=", path}}
	if err := v.client.Call(ctx, "sharing.nfs.query", []any{filter}, &shares); err != nil {
		return false, fmt.Errorf("failed to query NFS shares: %w", err)
	}
	return len(shares) > 0, nil
}

// NVMeOFSubsystemExists checks if an NVMe-oF subsystem exists with the given NQN.
// Note: TrueNAS uses "nvmet.subsys" API namespace, not "nvmeof.subsystem".
func (v *TrueNASVerifier) NVMeOFSubsystemExists(ctx context.Context, nqn string) (bool, error) {
	var subsystems []map[string]any
	filter := []any{[]any{"name", "=", nqn}}
	// Try nvmet.subsys.query first (current TrueNAS API)
	if err := v.client.Call(ctx, "nvmet.subsys.query", []any{filter}, &subsystems); err != nil {
		return false, fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
	}
	return len(subsystems) > 0, nil
}

// DeleteDataset deletes a dataset from TrueNAS.
// This is used for cleaning up retained datasets after tests.
func (v *TrueNASVerifier) DeleteDataset(ctx context.Context, datasetPath string) error {
	var result any
	if err := v.client.Call(ctx, "pool.dataset.delete", []any{datasetPath}, &result); err != nil {
		return fmt.Errorf("failed to delete dataset %s: %w", datasetPath, err)
	}
	return nil
}

// deleteResourceByFilter is a helper that queries for a resource by filter, gets its ID, and deletes it.
func (v *TrueNASVerifier) deleteResourceByFilter(
	ctx context.Context,
	queryMethod string,
	deleteMethod string,
	filterKey string,
	filterValue string,
	resourceDesc string,
) error {
	// Query for the resource
	var resources []map[string]any
	filter := []any{[]any{filterKey, "=", filterValue}}
	if err := v.client.Call(ctx, queryMethod, []any{filter}, &resources); err != nil {
		return fmt.Errorf("failed to query %s: %w", resourceDesc, err)
	}
	if len(resources) == 0 {
		// Resource doesn't exist, nothing to delete
		return nil
	}

	// Get the resource ID
	resourceID, ok := resources[0]["id"]
	if !ok {
		return fmt.Errorf("%s: %w", resourceDesc, ErrMissingIDField)
	}

	// Delete the resource
	var result any
	if err := v.client.Call(ctx, deleteMethod, []any{resourceID}, &result); err != nil {
		return fmt.Errorf("failed to delete %s (id=%v): %w", resourceDesc, resourceID, err)
	}
	return nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem from TrueNAS.
// This is used for cleaning up retained NVMe-oF subsystems after tests.
// Note: TrueNAS uses "nvmet.subsys" API namespace, not "nvmeof.subsystem".
// The filter key is "name" (which contains the NQN), not "nqn".
//
// TrueNAS requires the following order for deletion:
//  1. Delete all namespaces attached to the subsystem.
//  2. Remove all port associations (port-subsystem bindings).
//  3. Delete the subsystem itself.
func (v *TrueNASVerifier) DeleteNVMeOFSubsystem(ctx context.Context, nqn string) error {
	// Step 1: Query the subsystem to get its ID
	var subsystems []map[string]any
	filter := []any{[]any{"name", "=", nqn}}
	if err := v.client.Call(ctx, "nvmet.subsys.query", []any{filter}, &subsystems); err != nil {
		return fmt.Errorf("failed to query NVMe-oF subsystem: %w", err)
	}
	if len(subsystems) == 0 {
		// Subsystem doesn't exist, nothing to delete
		return nil
	}

	subsystemID, ok := subsystems[0]["id"]
	if !ok {
		return fmt.Errorf("NVMe-oF subsystem %s: %w", nqn, ErrMissingIDField)
	}

	// Convert subsystemID to int (JSON numbers come as float64)
	subsystemIDInt, err := toInt(subsystemID)
	if err != nil {
		return fmt.Errorf("invalid subsystem ID type: %w", err)
	}

	// Step 2: Delete all namespaces attached to this subsystem
	if err := v.deleteRelatedResources(ctx, subsystemIDInt, "nvmet.namespace.query", "nvmet.namespace.delete", "subsys", "namespace"); err != nil {
		return fmt.Errorf("failed to delete namespaces for subsystem %s: %w", nqn, err)
	}

	// Step 3: Remove all port associations for this subsystem
	// Note: TrueNAS uses underscore in port_subsys API methods, not dot
	if err := v.deleteRelatedResources(ctx, subsystemIDInt, "nvmet.port_subsys.query", "nvmet.port_subsys.delete", "subsys", "port binding"); err != nil {
		return fmt.Errorf("failed to remove port bindings for subsystem %s: %w", nqn, err)
	}

	// Step 4: Delete the subsystem itself
	var result any
	if err := v.client.Call(ctx, "nvmet.subsys.delete", []any{subsystemIDInt}, &result); err != nil {
		return fmt.Errorf("failed to delete NVMe-oF subsystem %s (id=%d): %w", nqn, subsystemIDInt, err)
	}

	return nil
}

// deleteRelatedResources deletes all resources that reference a parent resource ID.
// This is used to delete namespaces/port-bindings associated with a subsystem.
//
// TrueNAS API returns the parent reference (e.g., "subsys") as a nested object like:
//
//	{"id": 123, "name": "nqn...", "subnqn": "..."}
//
// NOT as a direct integer. This function handles both formats for robustness.
func (v *TrueNASVerifier) deleteRelatedResources(
	ctx context.Context,
	parentID int,
	queryMethod string,
	deleteMethod string,
	parentIDField string,
	resourceDesc string,
) error {
	// Query all resources
	var resources []map[string]any
	if err := v.client.Call(ctx, queryMethod, []any{}, &resources); err != nil {
		return fmt.Errorf("failed to query %ss: %w", resourceDesc, err)
	}

	// Find and delete resources belonging to the parent
	for _, res := range resources {
		// Check if this resource belongs to our parent
		resParentID, ok := res[parentIDField]
		if !ok {
			continue
		}

		// Extract parent ID - handle both nested object and direct int formats
		resParentIDInt, err := extractID(resParentID)
		if err != nil {
			continue
		}
		if resParentIDInt != parentID {
			continue
		}

		// Get the resource ID
		resID, ok := res["id"]
		if !ok {
			continue
		}
		resIDInt, err := toInt(resID)
		if err != nil {
			continue
		}

		// Delete this resource
		var result any
		if err := v.client.Call(ctx, deleteMethod, []any{resIDInt}, &result); err != nil {
			return fmt.Errorf("failed to delete %s %d: %w", resourceDesc, resIDInt, err)
		}
	}

	return nil
}

// toInt converts a value (typically from JSON unmarshaling) to int.
// JSON numbers are unmarshaled as float64 in Go.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, ErrInvalidIDType
	}
}

// extractID extracts an ID from a value that can be either:
// - A direct number (int, int64, float64)
// - A nested object with an "id" field (map[string]any)
//
// TrueNAS API returns parent references (like "subsys" in namespaces) as nested objects:
//
//	{"id": 123, "name": "nqn...", "subnqn": "..."}
func extractID(v any) (int, error) {
	// Try direct number first
	if id, err := toInt(v); err == nil {
		return id, nil
	}

	// Try nested object with "id" field
	if obj, ok := v.(map[string]any); ok {
		if idVal, exists := obj["id"]; exists {
			return toInt(idVal)
		}
	}

	return 0, ErrInvalidIDType
}

// DeleteNFSShare deletes an NFS share from TrueNAS.
// This is used for cleaning up retained NFS shares after tests.
func (v *TrueNASVerifier) DeleteNFSShare(ctx context.Context, path string) error {
	return v.deleteResourceByFilter(
		ctx,
		"sharing.nfs.query",
		"sharing.nfs.delete",
		"path",
		path,
		"NFS share for path "+path,
	)
}

// ISCSITargetExists checks if an iSCSI target exists with the given name.
func (v *TrueNASVerifier) ISCSITargetExists(ctx context.Context, targetName string) (bool, error) {
	var targets []map[string]any
	filter := []any{[]any{"name", "=", targetName}}
	if err := v.client.Call(ctx, "iscsi.target.query", []any{filter}, &targets); err != nil {
		return false, fmt.Errorf("failed to query iSCSI targets: %w", err)
	}
	return len(targets) > 0, nil
}

// ISCSIExtentExists checks if an iSCSI extent exists with the given name.
func (v *TrueNASVerifier) ISCSIExtentExists(ctx context.Context, extentName string) (bool, error) {
	var extents []map[string]any
	filter := []any{[]any{"name", "=", extentName}}
	if err := v.client.Call(ctx, "iscsi.extent.query", []any{filter}, &extents); err != nil {
		return false, fmt.Errorf("failed to query iSCSI extents: %w", err)
	}
	return len(extents) > 0, nil
}

// DeleteISCSITarget deletes an iSCSI target from TrueNAS.
// This is used for cleaning up retained iSCSI targets after tests.
func (v *TrueNASVerifier) DeleteISCSITarget(ctx context.Context, targetName string) error {
	// Query for the target first
	var targets []map[string]any
	filter := []any{[]any{"name", "=", targetName}}
	if err := v.client.Call(ctx, "iscsi.target.query", []any{filter}, &targets); err != nil {
		return fmt.Errorf("failed to query iSCSI target: %w", err)
	}
	if len(targets) == 0 {
		return nil // Target doesn't exist
	}

	targetID, ok := targets[0]["id"]
	if !ok {
		return fmt.Errorf("iSCSI target %s: %w", targetName, ErrMissingIDField)
	}

	targetIDInt, err := toInt(targetID)
	if err != nil {
		return fmt.Errorf("invalid target ID type: %w", err)
	}

	// Delete the target (force=true to delete associated resources)
	var result any
	if err := v.client.Call(ctx, "iscsi.target.delete", []any{targetIDInt, true}, &result); err != nil {
		return fmt.Errorf("failed to delete iSCSI target %s (id=%d): %w", targetName, targetIDInt, err)
	}
	return nil
}

// DeleteISCSIExtent deletes an iSCSI extent from TrueNAS.
// This is used for cleaning up retained iSCSI extents after tests.
func (v *TrueNASVerifier) DeleteISCSIExtent(ctx context.Context, extentName string) error {
	// Query for the extent first
	var extents []map[string]any
	filter := []any{[]any{"name", "=", extentName}}
	if err := v.client.Call(ctx, "iscsi.extent.query", []any{filter}, &extents); err != nil {
		return fmt.Errorf("failed to query iSCSI extent: %w", err)
	}
	if len(extents) == 0 {
		return nil // Extent doesn't exist
	}

	extentID, ok := extents[0]["id"]
	if !ok {
		return fmt.Errorf("iSCSI extent %s: %w", extentName, ErrMissingIDField)
	}

	extentIDInt, err := toInt(extentID)
	if err != nil {
		return fmt.Errorf("invalid extent ID type: %w", err)
	}

	// Delete the extent (remove_file=false, force=true)
	var result any
	params := map[string]any{
		"remove": false,
		"force":  true,
	}
	if err := v.client.Call(ctx, "iscsi.extent.delete", []any{extentIDInt, params}, &result); err != nil {
		return fmt.Errorf("failed to delete iSCSI extent %s (id=%d): %w", extentName, extentIDInt, err)
	}
	return nil
}

// GetDatasetProperty retrieves a specific ZFS user property from a dataset.
// Returns empty string if the property doesn't exist or is unset.
func (v *TrueNASVerifier) GetDatasetProperty(ctx context.Context, datasetPath, propertyName string) (string, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	// Request user properties to be included in the response
	options := map[string]any{
		"extra": map[string]any{
			"user_properties": true,
		},
	}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter, options}, &datasets); err != nil {
		return "", fmt.Errorf("failed to query dataset: %w", err)
	}
	if len(datasets) == 0 {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}

	// User properties are returned under the "user_properties" key
	dataset := datasets[0]
	userProps, ok := dataset["user_properties"]
	if !ok {
		return "", nil // No user properties
	}

	// user_properties is a map of property name -> {value, source, ...}
	propsMap, ok := userProps.(map[string]any)
	if !ok {
		return "", nil // Unexpected format
	}

	propData, ok := propsMap[propertyName]
	if !ok {
		return "", nil // Property not set
	}

	// Property value is in the "value" field
	if propMap, isMap := propData.(map[string]any); isMap {
		if val, hasValue := propMap["value"]; hasValue {
			if strVal, isStr := val.(string); isStr {
				return strVal, nil
			}
		}
	}

	return "", nil // Property not set or unexpected format
}
