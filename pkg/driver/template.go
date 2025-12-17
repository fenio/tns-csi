// Package driver provides volume name templating functionality for CSI volumes.
package driver

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"k8s.io/klog/v2"
)

// CSI parameter keys for PVC/PV information passed by Kubernetes.
// These are standard CSI parameters that Kubernetes populates automatically.
const (
	// CSIPVCName is the key for the PVC name in CSI parameters.
	CSIPVCName = "csi.storage.k8s.io/pvc/name"
	// CSIPVCNamespace is the key for the PVC namespace in CSI parameters.
	CSIPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	// CSIPVName is the key for the PV name in CSI parameters.
	CSIPVName = "csi.storage.k8s.io/pv/name"
)

// StorageClass parameter keys for name templating.
const (
	// ParamNameTemplate is the StorageClass parameter for full name template.
	// Example: "{{ .PVCNamespace }}-{{ .PVCName }}".
	ParamNameTemplate = "nameTemplate"
	// ParamNamePrefix is the StorageClass parameter for simple prefix.
	// Example: "prod-".
	ParamNamePrefix = "namePrefix"
	// ParamNameSuffix is the StorageClass parameter for simple suffix.
	// Example: "-data".
	ParamNameSuffix = "nameSuffix"
)

// VolumeNameContext holds the context variables available for name templating.
// These values are extracted from CSI CreateVolumeRequest parameters.
type VolumeNameContext struct {
	// PVCName is the name of the PersistentVolumeClaim (if available).
	PVCName string
	// PVCNamespace is the namespace of the PersistentVolumeClaim (if available).
	PVCNamespace string
	// PVName is the name of the PersistentVolume (CSI volume name).
	// This is always available as it comes from req.GetName().
	PVName string
}

// nameTemplateConfig holds parsed template configuration from StorageClass parameters.
type nameTemplateConfig struct {
	// template is the parsed Go template (nil if no template specified)
	template *template.Template
	// prefix is a simple prefix to prepend (used if no template)
	prefix string
	// suffix is a simple suffix to append (used if no template)
	suffix string
}

// Sentinel errors for volume name validation.
var (
	// ErrVolumeNameEmpty is returned when the volume name is empty after processing.
	ErrVolumeNameEmpty = errors.New("volume name cannot be empty")
	// ErrVolumeNameInvalid is returned when the volume name contains invalid characters.
	ErrVolumeNameInvalid = errors.New("invalid volume name: must start with alphanumeric and contain only alphanumeric, hyphen, underscore, colon, or period")
)

// validNameRegex matches valid ZFS dataset/zvol names.
// ZFS names can contain alphanumeric characters, hyphens, underscores, colons, and periods.
// They cannot start with a hyphen.
var validNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]*$`)

// parseNameTemplateConfig extracts name templating configuration from StorageClass parameters.
// Returns nil, nil if no templating is configured (use default naming).
//
//nolint:nilnil // nil, nil is the expected return when no templating is configured
func parseNameTemplateConfig(params map[string]string) (*nameTemplateConfig, error) {
	templateStr := params[ParamNameTemplate]
	prefix := params[ParamNamePrefix]
	suffix := params[ParamNameSuffix]

	// No templating configured - use default naming
	if templateStr == "" && prefix == "" && suffix == "" {
		return nil, nil
	}

	config := &nameTemplateConfig{
		prefix: prefix,
		suffix: suffix,
	}

	// Parse template if provided
	if templateStr != "" {
		tmpl, err := template.New("volumeName").Parse(templateStr)
		if err != nil {
			return nil, fmt.Errorf("invalid nameTemplate '%s': %w", templateStr, err)
		}
		config.template = tmpl
		klog.V(4).Infof("Parsed name template: %s", templateStr)
	}

	return config, nil
}

// extractVolumeNameContext extracts template context from CSI parameters.
// The pvName parameter should come from req.GetName() in CreateVolumeRequest.
func extractVolumeNameContext(params map[string]string, pvName string) VolumeNameContext {
	ctx := VolumeNameContext{
		PVName:       pvName,
		PVCName:      params[CSIPVCName],
		PVCNamespace: params[CSIPVCNamespace],
	}

	klog.V(5).Infof("Extracted volume name context: PVName=%s, PVCName=%s, PVCNamespace=%s",
		ctx.PVName, ctx.PVCName, ctx.PVCNamespace)

	return ctx
}

// renderVolumeName generates the final volume name using template configuration.
// If no templating is configured, returns the original pvName.
// The rendered name is sanitized to be valid for ZFS datasets/zvols.
func renderVolumeName(config *nameTemplateConfig, ctx VolumeNameContext) (string, error) {
	var name string

	if config == nil {
		// No templating - use PV name as-is
		return ctx.PVName, nil
	}

	if config.template != nil {
		// Use full template
		var buf bytes.Buffer
		if err := config.template.Execute(&buf, ctx); err != nil {
			return "", fmt.Errorf("failed to execute name template: %w", err)
		}
		name = buf.String()
	} else {
		// Use simple prefix/suffix
		name = config.prefix + ctx.PVName + config.suffix
	}

	// Sanitize the name for ZFS compatibility
	name = sanitizeVolumeName(name)

	// Validate the final name
	if err := validateVolumeName(name); err != nil {
		return "", err
	}

	klog.V(4).Infof("Rendered volume name: %s (from PVName=%s, PVCName=%s, PVCNamespace=%s)",
		name, ctx.PVName, ctx.PVCName, ctx.PVCNamespace)

	return name, nil
}

// sanitizeVolumeName cleans up a volume name to be valid for ZFS.
// It replaces invalid characters with hyphens, removes leading hyphens,
// and truncates to 63 characters for K8s label compatibility.
func sanitizeVolumeName(name string) string {
	// Replace common invalid characters with hyphens
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		" ", "-",
		"@", "-",
		"#", "-",
		"$", "-",
		"%", "-",
		"^", "-",
		"&", "-",
		"*", "-",
		"(", "-",
		")", "-",
		"+", "-",
		"=", "-",
		"[", "-",
		"]", "-",
		"{", "-",
		"}", "-",
		"|", "-",
		";", "-",
		"'", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		",", "-",
		"?", "-",
		"`", "-",
		"~", "-",
	)
	name = replacer.Replace(name)

	// Remove leading hyphens (ZFS names can't start with hyphen)
	name = strings.TrimLeft(name, "-")

	// Collapse multiple consecutive hyphens into one
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}

	// Trim trailing hyphens
	name = strings.TrimRight(name, "-")

	// Truncate to 63 characters (K8s label compatibility)
	if len(name) > 63 {
		name = name[:63]
		// Ensure we don't end with a hyphen after truncation
		name = strings.TrimRight(name, "-")
	}

	return name
}

// validateVolumeName checks if a volume name is valid for ZFS.
func validateVolumeName(name string) error {
	if name == "" {
		return ErrVolumeNameEmpty
	}

	if !validNameRegex.MatchString(name) {
		return fmt.Errorf("%w: '%s'", ErrVolumeNameInvalid, name)
	}

	return nil
}

// ResolveVolumeName is the main entry point for volume name resolution.
// It extracts templating configuration from StorageClass parameters,
// builds the template context, and renders the final volume name.
//
// Parameters:
//   - params: StorageClass parameters from the CreateVolumeRequest
//   - pvName: The PV name from req.GetName()
//
// Returns:
//   - The resolved volume name (may be same as pvName if no templating configured)
//   - An error if template parsing or rendering fails
func ResolveVolumeName(params map[string]string, pvName string) (string, error) {
	// Parse template configuration from StorageClass parameters
	config, err := parseNameTemplateConfig(params)
	if err != nil {
		return "", err
	}

	// Extract context from CSI parameters
	ctx := extractVolumeNameContext(params, pvName)

	// Render the final name
	return renderVolumeName(config, ctx)
}
