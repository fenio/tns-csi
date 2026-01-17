package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for troubleshoot command.
var errUnexpectedOutputFormat = errors.New("unexpected output format from kubectl")

// Status constants.
const (
	statusOK      = "OK"
	statusWarning = "Warning"
	statusError   = "Error"
	statusSkipped = "Skipped"
)

// TroubleshootResult contains the results of troubleshooting a PVC.
type TroubleshootResult struct {
	PVCName        string              `json:"pvcName"                  yaml:"pvcName"`
	Namespace      string              `json:"namespace"                yaml:"namespace"`
	Summary        string              `json:"summary"                  yaml:"summary"`
	Status         string              `json:"status"                   yaml:"status"`
	Checks         []TroubleshootCheck `json:"checks"                   yaml:"checks"`
	Suggestions    []string            `json:"suggestions"              yaml:"suggestions"`
	Events         []string            `json:"events"                   yaml:"events"`
	ControllerLogs []string            `json:"controllerLogs,omitempty" yaml:"controllerLogs,omitempty"`
}

// TroubleshootCheck represents a single troubleshooting check.
type TroubleshootCheck struct {
	Name    string `json:"name"    yaml:"name"`
	Status  string `json:"status"  yaml:"status"`
	Message string `json:"message" yaml:"message"`
}

// PVCInfo contains PVC information from kubectl.
type PVCInfo struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Status       string `json:"status"`
	Volume       string `json:"volume"`
	StorageClass string `json:"storageClass"`
	Capacity     string `json:"capacity"`
}

// PVInfo contains PV information from kubectl.
type PVInfo struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Claim        string `json:"claim"`
	StorageClass string `json:"storageClass"`
	VolumeHandle string `json:"volumeHandle"`
	Capacity     string `json:"capacity"`
}

func newTroubleshootCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var namespace string
	var showLogs bool

	cmd := &cobra.Command{
		Use:   "troubleshoot <pvc-name>",
		Short: "Diagnose issues with a PVC",
		Long: `Diagnose why a PVC isn't working properly.

This command performs comprehensive checks:
  - Kubernetes: PVC status, PV binding, events
  - TrueNAS: Dataset exists, NFS share/NVMe-oF subsystem status
  - Provides actionable suggestions based on findings

Examples:
  # Troubleshoot a PVC in default namespace
  kubectl tns-csi troubleshoot my-pvc

  # Troubleshoot a PVC in specific namespace
  kubectl tns-csi troubleshoot my-pvc -n my-namespace

  # Include CSI controller logs in output
  kubectl tns-csi troubleshoot my-pvc --logs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTroubleshoot(cmd.Context(), args[0], namespace, url, apiKey, secretRef, outputFormat, skipTLSVerify, showLogs)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")
	cmd.Flags().BoolVar(&showLogs, "logs", false, "Include CSI controller logs in output")
	return cmd
}

func runTroubleshoot(ctx context.Context, pvcName, namespace string, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, showLogs bool) error {
	result := &TroubleshootResult{
		PVCName:     pvcName,
		Namespace:   namespace,
		Checks:      make([]TroubleshootCheck, 0),
		Suggestions: make([]string, 0),
		Events:      make([]string, 0),
	}

	// Step 1: Check PVC in Kubernetes
	pvc, pvcErr := getPVCInfo(ctx, pvcName, namespace)
	if pvcErr != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "PVC Exists",
			Status:  statusError,
			Message: "PVC not found: " + pvcErr.Error(),
		})
		result.Suggestions = append(result.Suggestions, "Verify the PVC name and namespace are correct")
		result.Status = statusError
		result.Summary = "PVC not found in Kubernetes"
		return outputTroubleshootResult(result, *outputFormat)
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "PVC Exists",
		Status:  statusOK,
		Message: fmt.Sprintf("PVC found (status: %s)", pvc.Status),
	})

	// Step 2: Check PVC status
	if pvc.Status != "Bound" {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "PVC Bound",
			Status:  statusError,
			Message: fmt.Sprintf("PVC is not bound (status: %s)", pvc.Status),
		})
		result.Suggestions = append(result.Suggestions, "Check if StorageClass '"+pvc.StorageClass+"' exists and is configured correctly")
		result.Suggestions = append(result.Suggestions, "Check CSI controller logs for provisioning errors")

		// Get events for unbound PVC
		events := getPVCEvents(ctx, pvcName, namespace)
		result.Events = events
		if len(events) > 0 {
			result.Suggestions = append(result.Suggestions, "Review the events above for specific error messages")
		}

		result.Status = statusError
		result.Summary = "PVC is not bound - volume provisioning may have failed"

		if showLogs {
			result.ControllerLogs = getControllerLogs(ctx, pvcName)
		}

		return outputTroubleshootResult(result, *outputFormat)
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "PVC Bound",
		Status:  statusOK,
		Message: "PVC is bound to PV " + pvc.Volume,
	})

	// Step 3: Check PV
	pv, pvErr := getPVInfo(ctx, pvc.Volume)
	if pvErr != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "PV Exists",
			Status:  statusError,
			Message: "PV not found: " + pvErr.Error(),
		})
		result.Status = statusError
		result.Summary = "PV referenced by PVC does not exist"
		return outputTroubleshootResult(result, *outputFormat)
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "PV Exists",
		Status:  statusOK,
		Message: fmt.Sprintf("PV found (handle: %s)", pv.VolumeHandle),
	})

	// Step 4: Connect to TrueNAS and check resources
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "TrueNAS Connection",
			Status:  statusError,
			Message: "Cannot connect to TrueNAS: " + err.Error(),
		})
		result.Suggestions = append(result.Suggestions, "Verify TrueNAS connection settings (--url, --api-key, or --secret)")
		result.Status = statusWarning
		result.Summary = "Cannot verify TrueNAS resources - connection failed"
		return outputTroubleshootResult(result, *outputFormat)
	}

	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "TrueNAS Connection",
			Status:  statusError,
			Message: "Cannot connect to TrueNAS: " + err.Error(),
		})
		result.Suggestions = append(result.Suggestions, "Check TrueNAS is accessible and API key is valid")
		result.Status = statusWarning
		result.Summary = "Cannot verify TrueNAS resources - connection failed"
		return outputTroubleshootResult(result, *outputFormat)
	}
	defer client.Close()

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "TrueNAS Connection",
		Status:  statusOK,
		Message: "Connected to TrueNAS",
	})

	// Step 5: Check dataset on TrueNAS
	volumeID := pv.VolumeHandle
	dataset, datasetErr := findDatasetByVolumeID(ctx, client, volumeID)
	if datasetErr != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "TrueNAS Dataset",
			Status:  statusError,
			Message: "Dataset not found for volume " + volumeID,
		})
		result.Suggestions = append(result.Suggestions, "The dataset may have been deleted from TrueNAS")
		result.Suggestions = append(result.Suggestions, "Check if the volume was manually deleted or if there's a pool issue")
		result.Status = statusError
		result.Summary = "Volume dataset missing from TrueNAS"
		return outputTroubleshootResult(result, *outputFormat)
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "TrueNAS Dataset",
		Status:  statusOK,
		Message: "Dataset found: " + dataset.ID,
	})

	// Step 6: Check protocol-specific resources
	protocol := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyProtocol]; ok {
		protocol = prop.Value
	}

	switch protocol {
	case protocolNFS:
		checkNFSResourcesForTroubleshoot(ctx, client, dataset, result)
	case protocolNVMeOF:
		checkNVMeOFResourcesForTroubleshoot(ctx, client, dataset, result)
	default:
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "Protocol Check",
			Status:  statusWarning,
			Message: "Unknown protocol: " + protocol,
		})
	}

	// Step 7: Get events
	events := getPVCEvents(ctx, pvcName, namespace)
	result.Events = events

	// Check for warning events
	hasWarningEvents := false
	for _, event := range events {
		if strings.Contains(event, "Warning") {
			hasWarningEvents = true
			break
		}
	}

	if hasWarningEvents {
		result.Suggestions = append(result.Suggestions, "Review warning events above for potential issues")
	}

	// Step 8: Get controller logs if requested
	if showLogs {
		result.ControllerLogs = getControllerLogs(ctx, volumeID)
	}

	// Determine final status
	hasErrors := false
	hasWarnings := false
	for i := range result.Checks {
		switch result.Checks[i].Status {
		case statusError:
			hasErrors = true
		case statusWarning:
			hasWarnings = true
		}
	}

	switch {
	case hasErrors:
		result.Status = statusError
		result.Summary = "Issues found - see checks above"
	case hasWarnings:
		result.Status = statusWarning
		result.Summary = "Some warnings found but volume should be functional"
	default:
		result.Status = statusOK
		result.Summary = "All checks passed - volume appears healthy"
		if len(result.Suggestions) == 0 {
			result.Suggestions = append(result.Suggestions, "If you're still experiencing issues, check pod events and node logs")
		}
	}

	return outputTroubleshootResult(result, *outputFormat)
}

// findDatasetByVolumeID finds a dataset by volume ID (CSI volume name).
func findDatasetByVolumeID(ctx context.Context, client tnsapi.ClientInterface, volumeID string) (*tnsapi.DatasetWithProperties, error) {
	// Try to find by CSI volume name
	return client.FindDatasetByCSIVolumeName(ctx, "", volumeID)
}

// checkNFSResourcesForTroubleshoot checks NFS-specific resources on TrueNAS.
func checkNFSResourcesForTroubleshoot(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties, result *TroubleshootResult) {
	sharePath := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if dataset.Mountpoint != "" {
		sharePath = dataset.Mountpoint
	}

	if sharePath == "" {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NFS Share Path",
			Status:  statusWarning,
			Message: "No share path found in volume properties",
		})
		return
	}

	shares, err := client.QueryNFSShare(ctx, sharePath)
	if err != nil || len(shares) == 0 {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NFS Share",
			Status:  statusError,
			Message: "NFS share not found for path " + sharePath,
		})
		result.Suggestions = append(result.Suggestions, "The NFS share may have been deleted - recreate it or delete/recreate the PVC")
		return
	}

	share := shares[0]
	if !share.Enabled {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NFS Share",
			Status:  statusError,
			Message: "NFS share exists but is disabled",
		})
		result.Suggestions = append(result.Suggestions, "Enable the NFS share in TrueNAS UI or via API")
		return
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "NFS Share",
		Status:  statusOK,
		Message: fmt.Sprintf("NFS share found and enabled (ID: %d)", share.ID),
	})
}

// checkNVMeOFResourcesForTroubleshoot checks NVMe-oF-specific resources on TrueNAS.
func checkNVMeOFResourcesForTroubleshoot(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties, result *TroubleshootResult) {
	nqn := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

	if nqn == "" {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NVMe-oF Subsystem NQN",
			Status:  statusWarning,
			Message: "No subsystem NQN found in volume properties",
		})
		return
	}

	subsystem, err := client.NVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NVMe-oF Subsystem",
			Status:  statusError,
			Message: "NVMe-oF subsystem not found: " + nqn,
		})
		result.Suggestions = append(result.Suggestions, "The NVMe-oF subsystem may have been deleted - delete/recreate the PVC")
		return
	}

	if !subsystem.Enabled {
		result.Checks = append(result.Checks, TroubleshootCheck{
			Name:    "NVMe-oF Subsystem",
			Status:  statusError,
			Message: "NVMe-oF subsystem exists but is disabled",
		})
		result.Suggestions = append(result.Suggestions, "Enable the NVMe-oF subsystem in TrueNAS UI or via API")
		return
	}

	result.Checks = append(result.Checks, TroubleshootCheck{
		Name:    "NVMe-oF Subsystem",
		Status:  statusOK,
		Message: fmt.Sprintf("NVMe-oF subsystem found and enabled (ID: %d, NQN: %s)", subsystem.ID, subsystem.NQN),
	})
}

// getPVCInfo gets PVC information from Kubernetes.
func getPVCInfo(ctx context.Context, name, namespace string) (*PVCInfo, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pvc", name,
		"-n", namespace,
		"-o", "jsonpath={.metadata.name},{.metadata.namespace},{.status.phase},{.spec.volumeName},{.spec.storageClassName},{.status.capacity.storage}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	parts := strings.Split(string(output), ",")
	if len(parts) < 5 {
		return nil, errUnexpectedOutputFormat
	}

	return &PVCInfo{
		Name:         parts[0],
		Namespace:    parts[1],
		Status:       parts[2],
		Volume:       parts[3],
		StorageClass: parts[4],
		Capacity:     safeIndex(parts, 5),
	}, nil
}

// getPVInfo gets PV information from Kubernetes.
func getPVInfo(ctx context.Context, name string) (*PVInfo, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pv", name,
		"-o", "jsonpath={.metadata.name},{.status.phase},{.spec.claimRef.namespace}/{.spec.claimRef.name},{.spec.storageClassName},{.spec.csi.volumeHandle},{.spec.capacity.storage}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	parts := strings.Split(string(output), ",")
	if len(parts) < 5 {
		return nil, errUnexpectedOutputFormat
	}

	return &PVInfo{
		Name:         parts[0],
		Status:       parts[1],
		Claim:        parts[2],
		StorageClass: parts[3],
		VolumeHandle: parts[4],
		Capacity:     safeIndex(parts, 5),
	}, nil
}

// safeIndex safely gets an index from a slice, returning empty string if out of bounds.
func safeIndex(slice []string, index int) string {
	if index < len(slice) {
		return slice[index]
	}
	return ""
}

// getPVCEvents gets recent events for a PVC.
func getPVCEvents(ctx context.Context, pvcName, namespace string) []string {
	//nolint:gosec // G204: pvcName and namespace are user-provided to this kubectl plugin - this is expected behavior
	cmd := exec.CommandContext(ctx, "kubectl", "get", "events",
		"-n", namespace,
		"--field-selector", "involvedObject.name="+pvcName,
		"--sort-by=.lastTimestamp",
		"-o", "custom-columns=TIME:.lastTimestamp,TYPE:.type,REASON:.reason,MESSAGE:.message",
		"--no-headers")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	// Filter out empty lines
	var events []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			events = append(events, line)
		}
	}

	// Return last 10 events
	if len(events) > 10 {
		events = events[len(events)-10:]
	}
	return events
}

// getControllerLogs gets CSI controller logs filtered by volume ID.
func getControllerLogs(ctx context.Context, volumeID string) []string {
	cmd := exec.CommandContext(ctx, "kubectl", "logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller",
		"--tail=200")
	output, err := cmd.Output()
	if err != nil {
		return []string{"Failed to get controller logs: " + err.Error()}
	}

	// Filter lines containing the volume ID
	lines := strings.Split(string(output), "\n")
	var filtered []string
	for _, line := range lines {
		if strings.Contains(line, volumeID) {
			filtered = append(filtered, line)
		}
	}

	// Return last 20 relevant lines
	if len(filtered) > 20 {
		filtered = filtered[len(filtered)-20:]
	}

	if len(filtered) == 0 {
		return []string{"No log entries found for volume " + volumeID}
	}

	return filtered
}

// outputTroubleshootResult outputs the troubleshoot result in the specified format.
func outputTroubleshootResult(result *TroubleshootResult, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(result)

	case outputFormatTable, "":
		return outputTroubleshootResultTable(result)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// outputTroubleshootResultTable outputs the troubleshoot result in table format.
func outputTroubleshootResultTable(result *TroubleshootResult) error {
	// Header
	statusIcon := "✓"
	switch result.Status {
	case statusError:
		statusIcon = "✗"
	case statusWarning:
		statusIcon = "!"
	}

	fmt.Printf("=== Troubleshoot: %s/%s ===\n", result.Namespace, result.PVCName)
	fmt.Printf("Status: %s %s\n", statusIcon, result.Summary)
	fmt.Println()

	// Checks
	fmt.Println("=== Checks ===")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i := range result.Checks {
		check := &result.Checks[i]
		icon := "✓"
		switch check.Status {
		case statusError:
			icon = "✗"
		case statusWarning:
			icon = "!"
		case statusSkipped:
			icon = "-"
		}
		//nolint:errcheck // writing to tabwriter for stdout
		_, _ = fmt.Fprintf(w, "%s %s\t%s\n", icon, check.Name, check.Message)
	}
	//nolint:errcheck // flushing tabwriter for stdout
	_ = w.Flush()
	fmt.Println()

	// Suggestions
	if len(result.Suggestions) > 0 {
		fmt.Println("=== Suggestions ===")
		for i, suggestion := range result.Suggestions {
			fmt.Printf("%d. %s\n", i+1, suggestion)
		}
		fmt.Println()
	}

	// Events
	if len(result.Events) > 0 {
		fmt.Println("=== Recent Events ===")
		for _, event := range result.Events {
			fmt.Println(event)
		}
		fmt.Println()
	}

	// Controller logs
	if len(result.ControllerLogs) > 0 {
		fmt.Println("=== Controller Logs ===")
		var buf bytes.Buffer
		for _, line := range result.ControllerLogs {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
		fmt.Print(buf.String())
	}

	return nil
}
