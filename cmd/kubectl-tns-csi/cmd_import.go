package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for import command.
var (
	errInvalidProtocol     = errors.New("invalid protocol: must be 'nfs' or 'nvmeof'")
	errAlreadyManaged      = errors.New("dataset is already managed by tns-csi")
	errNoNFSShareForImport = errors.New("no NFS share found, use --create-share to create one")
	errPoolOrParentMissing = errors.New("either --pool or --parent must be specified")
)

// ImportResult contains the result of the import operation.
//
//nolint:govet // field alignment not critical for CLI output struct
type ImportResult struct {
	Dataset       string            `json:"dataset"                yaml:"dataset"`
	VolumeID      string            `json:"volumeId"               yaml:"volumeId"`
	Protocol      string            `json:"protocol"               yaml:"protocol"`
	NFSShareID    int               `json:"nfsShareId,omitempty"   yaml:"nfsShareId,omitempty"`
	NFSSharePath  string            `json:"nfsSharePath,omitempty" yaml:"nfsSharePath,omitempty"`
	CapacityBytes int64             `json:"capacityBytes"          yaml:"capacityBytes"`
	Properties    map[string]string `json:"properties"             yaml:"properties"`
	Success       bool              `json:"success"                yaml:"success"`
	Message       string            `json:"message"                yaml:"message"`
}

func newImportCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		protocol     string
		volumeID     string
		createShare  bool
		storageClass string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "import <dataset-path>",
		Short: "Import an existing dataset into tns-csi management",
		Long: `Import an existing TrueNAS dataset into tns-csi management.

This command adds tns-csi properties to an existing dataset, allowing it to be
managed by the tns-csi driver. This is useful for:
  - Migrating volumes from democratic-csi
  - Adopting manually created datasets
  - Taking over volumes from other CSI drivers

The command will:
  1. Verify the dataset exists
  2. Detect or create NFS share (for NFS protocol)
  3. Add tns-csi management properties
  4. Prepare the volume for adoption into Kubernetes

After importing, use 'kubectl tns-csi adopt <dataset>' to generate PV/PVC manifests.

Examples:
  # Import an NFS dataset (auto-detect existing share)
  kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs

  # Import and create NFS share if missing
  kubectl tns-csi import storage/data/myvolume --protocol nfs --create-share

  # Import with custom volume ID
  kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs --volume-id my-volume

  # Dry run to see what would happen
  kubectl tns-csi import storage/k8s/pvc-xxx --protocol nfs --dry-run

  # Import a zvol for NVMe-oF (future support)
  kubectl tns-csi import storage/zvols/myvol --protocol nvmeof`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetPath := args[0]
			return runImport(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify,
				datasetPath, protocol, volumeID, createShare, storageClass, dryRun)
		},
	}

	cmd.Flags().StringVar(&protocol, "protocol", "", "Protocol: nfs or nvmeof (required)")
	cmd.Flags().StringVar(&volumeID, "volume-id", "", "Custom volume ID (defaults to dataset name)")
	cmd.Flags().BoolVar(&createShare, "create-share", false, "Create NFS share if it doesn't exist")
	cmd.Flags().StringVar(&storageClass, "storage-class", "", "StorageClass to associate with the volume")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")

	//nolint:errcheck,gosec // MarkFlagRequired doesn't fail for valid flag names
	cmd.MarkFlagRequired("protocol")

	return cmd
}

func runImport(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool,
	datasetPath, protocol, volumeID string, createShare bool, storageClass string, dryRun bool) error {

	// Validate protocol
	if protocol != protocolNFS && protocol != protocolNVMeOF {
		return fmt.Errorf("%w: %s", errInvalidProtocol, protocol)
	}

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

	// Verify dataset exists
	dataset, err := client.Dataset(ctx, datasetPath)
	if err != nil {
		return fmt.Errorf("dataset not found: %w", err)
	}

	// Check if already managed by tns-csi
	existingProps, err := client.GetAllDatasetProperties(ctx, datasetPath)
	if err == nil {
		if val, ok := existingProps[tnsapi.PropertyManagedBy]; ok && val == tnsapi.ManagedByValue {
			return fmt.Errorf("%w: %s", errAlreadyManaged, datasetPath)
		}
	}

	// Prepare result
	result := &ImportResult{
		Dataset:    datasetPath,
		Protocol:   protocol,
		Properties: make(map[string]string),
	}

	// Determine volume ID
	if volumeID == "" {
		volumeID = extractDatasetName(datasetPath)
	}
	result.VolumeID = volumeID

	// Get capacity from used space
	if dataset.Used != nil {
		if val, ok := dataset.Used["parsed"].(float64); ok {
			result.CapacityBytes = int64(val)
		}
	}

	// Build properties to set
	props := map[string]string{
		tnsapi.PropertyManagedBy:     tnsapi.ManagedByValue,
		tnsapi.PropertyCSIVolumeName: volumeID,
		tnsapi.PropertyProtocol:      protocol,
		tnsapi.PropertyCapacityBytes: strconv.FormatInt(result.CapacityBytes, 10),
		tnsapi.PropertyAdoptable:     tnsapi.PropertyValueTrue, // Mark as adoptable
	}

	if storageClass != "" {
		props[tnsapi.PropertyStorageClass] = storageClass
	}

	// Protocol-specific handling
	switch protocol {
	case protocolNFS:
		nfsProps, nfsErr := handleNFSImport(ctx, client, dataset, createShare, dryRun)
		if nfsErr != nil {
			return fmt.Errorf("NFS setup failed: %w", nfsErr)
		}
		for k, v := range nfsProps {
			if k == "_nfs_share_id" {
				//nolint:errcheck // ignore parse errors for internal metadata
				result.NFSShareID, _ = strconv.Atoi(v)
			} else {
				props[k] = v
			}
		}
		if sharePath, ok := nfsProps[tnsapi.PropertyNFSSharePath]; ok {
			result.NFSSharePath = sharePath
		}

	case protocolNVMeOF:
		// NVMe-oF import would need subsystem handling
		// For now, just warn that it's not fully supported
		fmt.Fprintln(os.Stderr, "Warning: NVMe-oF import is experimental. Subsystem must already exist.")
	}

	result.Properties = props

	if dryRun {
		result.Success = true
		result.Message = "Dry run - no changes made"
		fmt.Println("DRY RUN - Would set the following properties:")
		for k, v := range props {
			fmt.Printf("  %s = %s\n", k, v)
		}
		return outputImportResult(result, *outputFormat)
	}

	// Apply properties
	err = client.SetDatasetProperties(ctx, datasetPath, props)
	if err != nil {
		result.Success = false
		result.Message = "Failed to set properties: " + err.Error()
		return outputImportResult(result, *outputFormat)
	}

	result.Success = true
	result.Message = "Volume imported successfully"

	if err := outputImportResult(result, *outputFormat); err != nil {
		return err
	}

	// Print next steps for table format
	if *outputFormat == "" || *outputFormat == outputFormatTable {
		fmt.Println("\nNext steps:")
		fmt.Printf("  kubectl tns-csi adopt %s --pvc-name <name> --namespace <ns>\n", datasetPath)
	}

	return nil
}

func handleNFSImport(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.Dataset, createShare, dryRun bool) (map[string]string, error) {
	props := make(map[string]string)

	// Check for existing NFS share
	shares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	var existingShare *tnsapi.NFSShare
	for i := range shares {
		if shares[i].Path == dataset.Mountpoint {
			existingShare = &shares[i]
			break
		}
	}

	if existingShare != nil {
		// Use existing share
		props[tnsapi.PropertyNFSSharePath] = existingShare.Path
		props[tnsapi.PropertyNFSShareID] = strconv.Itoa(existingShare.ID)
		props["_nfs_share_id"] = strconv.Itoa(existingShare.ID)
		fmt.Printf("Found existing NFS share: %s (ID: %d)\n", existingShare.Path, existingShare.ID)
		return props, nil
	}

	// No existing share
	if !createShare {
		return nil, fmt.Errorf("%w: %s", errNoNFSShareForImport, dataset.Mountpoint)
	}

	if dryRun {
		fmt.Printf("DRY RUN - Would create NFS share for path: %s\n", dataset.Mountpoint)
		props[tnsapi.PropertyNFSSharePath] = dataset.Mountpoint
		return props, nil
	}

	// Create NFS share
	shareParams := tnsapi.NFSShareCreateParams{
		Path:         dataset.Mountpoint,
		Comment:      "tns-csi imported volume: " + dataset.ID,
		Enabled:      true,
		MaprootUser:  "root",
		MaprootGroup: "wheel",
	}

	share, err := client.CreateNFSShare(ctx, shareParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create NFS share: %w", err)
	}

	props[tnsapi.PropertyNFSSharePath] = dataset.Mountpoint
	props[tnsapi.PropertyNFSShareID] = strconv.Itoa(share.ID)
	props["_nfs_share_id"] = strconv.Itoa(share.ID)

	fmt.Printf("Created NFS share: %s (ID: %d)\n", dataset.Mountpoint, share.ID)
	return props, nil
}

func outputImportResult(result *ImportResult, format string) error {
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
		if result.Success {
			fmt.Printf("Successfully imported %s\n", result.Dataset)
			fmt.Printf("  Volume ID: %s\n", result.VolumeID)
			fmt.Printf("  Protocol:  %s\n", result.Protocol)
			fmt.Printf("  Capacity:  %s\n", formatBytes(result.CapacityBytes))
			if result.NFSSharePath != "" {
				fmt.Printf("  NFS Share: %s (ID: %d)\n", result.NFSSharePath, result.NFSShareID)
			}
		} else {
			fmt.Printf("Failed to import %s: %s\n", result.Dataset, result.Message)
		}
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
