package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for adopt command.
var (
	errDatasetNotFound    = errors.New("dataset not found")
	errNoUserProperties   = errors.New("no user properties found")
	errNotManagedByTNSCSI = errors.New("not managed by tns-csi")
)

func newAdoptCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		pvcName      string
		namespace    string
		storageClass string
		accessMode   string
	)

	cmd := &cobra.Command{
		Use:   "adopt <dataset-path>",
		Short: "Generate static PV/PVC manifests to adopt an orphaned volume",
		Long: `Generate Kubernetes PersistentVolume and PersistentVolumeClaim manifests
for adopting an orphaned volume into the cluster.

The generated manifests use the static provisioning pattern - the PV references
the existing TrueNAS dataset, and the PVC binds to it.

Examples:
  # Generate manifests for a specific dataset
  kubectl tns-csi adopt tank/csi/pvc-abc123 --pvc-name my-data --namespace default

  # Use stored PVC metadata from volume properties
  kubectl tns-csi adopt tank/csi/pvc-abc123

  # Output as single YAML document
  kubectl tns-csi adopt tank/csi/pvc-abc123 -o yaml > adopt.yaml
  kubectl apply -f adopt.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetPath := args[0]
			return runAdopt(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify,
				datasetPath, pvcName, namespace, storageClass, accessMode)
		},
	}

	cmd.Flags().StringVar(&pvcName, "pvc-name", "", "PVC name (defaults to volume's stored pvc_name or volume ID)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the PVC")
	cmd.Flags().StringVar(&storageClass, "storage-class", "", "StorageClass name (defaults to volume's stored storage_class)")
	cmd.Flags().StringVar(&accessMode, "access-mode", "", "Access mode: ReadWriteOnce, ReadWriteMany (auto-detected from protocol)")

	return cmd
}

func runAdopt(ctx context.Context, url, apiKey, secretRef, _ *string, skipTLSVerify *bool,
	datasetPath, pvcName, namespace, storageClass, accessMode string) error {

	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to TrueNAS
	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Get dataset info
	dataset, err := getDatasetWithProperties(ctx, client, datasetPath)
	if err != nil {
		return fmt.Errorf("failed to get dataset %s: %w", datasetPath, err)
	}

	// Extract volume metadata
	volumeInfo, err := extractVolumeInfo(dataset)
	if err != nil {
		return fmt.Errorf("dataset %s is not a valid tns-csi volume: %w", datasetPath, err)
	}

	// Apply overrides from flags
	if pvcName != "" {
		volumeInfo.pvcName = pvcName
	}
	if namespace != "" {
		volumeInfo.namespace = namespace
	}
	if storageClass != "" {
		volumeInfo.storageClass = storageClass
	}
	if accessMode != "" {
		volumeInfo.accessMode = accessMode
	}

	// Generate manifests
	manifests, err := generateAdoptionManifests(volumeInfo, cfg.URL)
	if err != nil {
		return fmt.Errorf("failed to generate manifests: %w", err)
	}

	// Output
	fmt.Println("# Generated adoption manifests for", datasetPath)
	fmt.Println("# Apply with: kubectl apply -f <file>")
	fmt.Println("---")
	fmt.Println(manifests)

	return nil
}

// adoptionVolumeInfo holds all info needed to generate adoption manifests.
type adoptionVolumeInfo struct {
	volumeID      string
	dataset       string
	protocol      string
	pvcName       string
	namespace     string
	storageClass  string
	accessMode    string
	nfsSharePath  string // NFS specific
	nvmeNQN       string // NVMe-oF specific
	capacityBytes int64
}

func getDatasetWithProperties(ctx context.Context, client tnsapi.ClientInterface, datasetPath string) (*tnsapi.DatasetWithProperties, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, datasetPath, tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	// Look for exact match
	for i := range datasets {
		if datasets[i].ID == datasetPath {
			return &datasets[i], nil
		}
	}

	// If no exact match, try querying directly
	// This handles cases where the dataset exists but might not have the property yet
	allDatasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyCSIVolumeName, "")
	if err != nil {
		return nil, err
	}

	for i := range allDatasets {
		if allDatasets[i].ID == datasetPath {
			return &allDatasets[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errDatasetNotFound, datasetPath)
}

func extractVolumeInfo(ds *tnsapi.DatasetWithProperties) (*adoptionVolumeInfo, error) {
	props := ds.UserProperties
	if props == nil {
		return nil, errNoUserProperties
	}

	// Verify it's managed by tns-csi
	if prop, ok := props[tnsapi.PropertyManagedBy]; !ok || prop.Value != tnsapi.ManagedByValue {
		return nil, errNotManagedByTNSCSI
	}

	info := &adoptionVolumeInfo{
		dataset:   ds.ID,
		namespace: "default", // Default
	}

	// Extract volume ID
	if prop, ok := props[tnsapi.PropertyCSIVolumeName]; ok {
		info.volumeID = prop.Value
		info.pvcName = prop.Value // Default PVC name to volume ID
	}

	// Extract protocol
	if prop, ok := props[tnsapi.PropertyProtocol]; ok {
		info.protocol = prop.Value
		// Set default access mode based on protocol
		switch prop.Value {
		case tnsapi.ProtocolNFS:
			info.accessMode = "ReadWriteMany"
		case tnsapi.ProtocolNVMeOF:
			info.accessMode = "ReadWriteOnce"
		}
	}

	// Extract capacity
	if prop, ok := props[tnsapi.PropertyCapacityBytes]; ok {
		info.capacityBytes = tnsapi.StringToInt64(prop.Value)
	}

	// Extract stored PVC metadata (if available from previous cluster)
	if prop, ok := props[tnsapi.PropertyPVCName]; ok && prop.Value != "" {
		info.pvcName = prop.Value
	}
	if prop, ok := props[tnsapi.PropertyPVCNamespace]; ok && prop.Value != "" {
		info.namespace = prop.Value
	}
	if prop, ok := props[tnsapi.PropertyStorageClass]; ok {
		info.storageClass = prop.Value
	}

	// Extract NFS-specific info
	if prop, ok := props[tnsapi.PropertyNFSSharePath]; ok {
		info.nfsSharePath = prop.Value
	}

	// Extract NVMe-oF-specific info
	if prop, ok := props[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		info.nvmeNQN = prop.Value
	}

	return info, nil
}

func generateAdoptionManifests(info *adoptionVolumeInfo, truenasURL string) (string, error) {
	// Extract server from TrueNAS URL for NFS
	server := extractServerFromURL(truenasURL)

	// Generate PV
	pv := generatePV(info, server)

	// Generate PVC
	pvc := generatePVC(info)

	// Combine
	pvYAML, err := yaml.Marshal(pv)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PV: %w", err)
	}

	pvcYAML, err := yaml.Marshal(pvc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PVC: %w", err)
	}

	return string(pvYAML) + "---\n" + string(pvcYAML), nil
}

func extractServerFromURL(url string) string {
	// Extract host from wss://host:port/path
	url = strings.TrimPrefix(url, "wss://")
	url = strings.TrimPrefix(url, "ws://")
	if idx := strings.Index(url, ":"); idx > 0 {
		return url[:idx]
	}
	if idx := strings.Index(url, "/"); idx > 0 {
		return url[:idx]
	}
	return url
}

func generatePV(info *adoptionVolumeInfo, server string) map[string]interface{} {
	pvName := "pv-" + info.volumeID

	// Build volume attributes based on protocol
	volumeAttributes := map[string]string{
		"protocol":    info.protocol,
		"datasetID":   info.dataset,
		"datasetName": info.dataset,
	}

	switch info.protocol {
	case tnsapi.ProtocolNFS:
		if info.nfsSharePath != "" {
			volumeAttributes["share"] = info.nfsSharePath
		}
		volumeAttributes["server"] = server

	case tnsapi.ProtocolNVMeOF:
		if info.nvmeNQN != "" {
			volumeAttributes["nqn"] = info.nvmeNQN
		}
		volumeAttributes["server"] = server
	}

	spec := map[string]interface{}{
		"capacity": map[string]string{
			"storage": formatBytesK8s(info.capacityBytes),
		},
		"accessModes":                   []string{info.accessMode},
		"persistentVolumeReclaimPolicy": "Retain", // Safe default for adopted volumes
		"csi": map[string]interface{}{
			"driver":           "tns.csi.io",
			"volumeHandle":     info.volumeID,
			"volumeAttributes": volumeAttributes,
		},
		"claimRef": map[string]interface{}{
			"name":      info.pvcName,
			"namespace": info.namespace,
		},
	}

	if info.storageClass != "" {
		spec["storageClassName"] = info.storageClass
	}

	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "PersistentVolume",
		"metadata": map[string]interface{}{
			"name": pvName,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "kubectl-tns-csi",
				"tns-csi.io/adopted":           "true",
			},
			"annotations": map[string]string{
				"tns-csi.io/dataset": info.dataset,
			},
		},
		"spec": spec,
	}
}

func generatePVC(info *adoptionVolumeInfo) map[string]interface{} {
	pvName := "pv-" + info.volumeID

	spec := map[string]interface{}{
		"accessModes": []string{info.accessMode},
		"resources": map[string]interface{}{
			"requests": map[string]string{
				"storage": formatBytesK8s(info.capacityBytes),
			},
		},
		"volumeName": pvName,
	}

	if info.storageClass != "" {
		spec["storageClassName"] = info.storageClass
	}

	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]interface{}{
			"name":      info.pvcName,
			"namespace": info.namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "kubectl-tns-csi",
				"tns-csi.io/adopted":           "true",
			},
			"annotations": map[string]string{
				"tns-csi.io/dataset": info.dataset,
			},
		},
		"spec": spec,
	}
}

// formatBytesK8s formats bytes in Kubernetes resource format.
func formatBytesK8s(bytes int64) string {
	const (
		Ki = 1024
		Mi = Ki * 1024
		Gi = Mi * 1024
		Ti = Gi * 1024
	)

	switch {
	case bytes >= Ti && bytes%Ti == 0:
		return fmt.Sprintf("%dTi", bytes/Ti)
	case bytes >= Gi && bytes%Gi == 0:
		return fmt.Sprintf("%dGi", bytes/Gi)
	case bytes >= Mi && bytes%Mi == 0:
		return fmt.Sprintf("%dMi", bytes/Mi)
	case bytes >= Ki && bytes%Ki == 0:
		return fmt.Sprintf("%dKi", bytes/Ki)
	default:
		// For non-aligned sizes, use Gi with decimal
		return fmt.Sprintf("%.2fGi", float64(bytes)/float64(Gi))
	}
}
