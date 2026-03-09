package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// Static errors for data operations.
var (
	errVolumeNotFound = errors.New("volume not found")
	errNoSharePath    = errors.New("no share path found")
	errNoNFSShare     = errors.New("no NFS share found")
	errNoSMBShare     = errors.New("no SMB share found")
	errNoSubsystemNQN = errors.New("no subsystem NQN found")
	errNoISCSIIQN     = errors.New("no iSCSI IQN found")
)

// FindManagedVolumes finds all datasets managed by tns-csi.
func FindManagedVolumes(ctx context.Context, client tnsapi.ClientInterface) ([]VolumeInfo, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}
	return extractVolumes(datasets), nil
}

// FindManagedSnapshots finds all snapshots managed by tns-csi.
func FindManagedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	var snapshots []SnapshotInfo

	attached, err := findAttachedSnapshots(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to find attached snapshots: %w", err)
	}
	snapshots = append(snapshots, attached...)

	detached, err := findDetachedSnapshots(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to find detached snapshots: %w", err)
	}
	snapshots = append(snapshots, detached...)

	return snapshots, nil
}

func findAttachedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	managedDatasets := make(map[string]struct {
		volumeID string
		protocol string
	})
	for _, ds := range datasets {
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		protocol := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			protocol = prop.Value
		}
		if volumeID != "" {
			managedDatasets[ds.ID] = struct {
				volumeID string
				protocol string
			}{volumeID: volumeID, protocol: protocol}
		}
	}

	var snapshots []SnapshotInfo
	for datasetID, meta := range managedDatasets {
		datasetSnaps, queryErr := client.QuerySnapshots(ctx, []interface{}{
			[]interface{}{"dataset", "=", datasetID},
		})
		if queryErr != nil {
			return nil, fmt.Errorf("failed to query snapshots for dataset %s: %w", datasetID, queryErr)
		}

		for _, snap := range datasetSnaps {
			snapshots = append(snapshots, SnapshotInfo{
				Name:          snap.Name,
				SourceVolume:  meta.volumeID,
				SourceDataset: snap.Dataset,
				Protocol:      meta.protocol,
				Type:          "attached",
			})
		}
	}

	return snapshots, nil
}

func findDetachedSnapshots(ctx context.Context, client tnsapi.ClientInterface) ([]SnapshotInfo, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyDetachedSnapshot, valueTrue)
	if err != nil {
		return nil, err
	}
	return extractDetachedSnapshots(datasets), nil
}

// FindClonedVolumes finds all volumes that were cloned from snapshots or other volumes.
func FindClonedVolumes(ctx context.Context, client tnsapi.ClientInterface) ([]CloneInfo, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}
	return extractClones(datasets), nil
}

// FindUnmanagedVolumes finds volumes not managed by tns-csi.
func FindUnmanagedVolumes(ctx context.Context, client tnsapi.ClientInterface, searchPath string, showAll bool) ([]UnmanagedVolume, error) {
	allDatasets, err := client.QueryAllDatasets(ctx, searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	managedDatasets, err := client.FindManagedDatasets(ctx, searchPath)
	if err != nil {
		managedDatasets = nil
	}

	managedIDs := make(map[string]bool)
	for i := range managedDatasets {
		managedIDs[managedDatasets[i].ID] = true
	}

	nfsShares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		nfsShares = nil
	}
	nfsShareByPath := make(map[string]*tnsapi.NFSShare)
	for i := range nfsShares {
		nfsShareByPath[nfsShares[i].Path] = &nfsShares[i]
	}

	//nolint:errcheck // non-fatal if this fails
	democraticDatasets, _ := client.FindDatasetsByProperty(ctx, searchPath, "democratic-csi:csi_share_volume_context", "")
	democraticIDs := make(map[string]string)
	for i := range democraticDatasets {
		democraticIDs[democraticDatasets[i].ID] = "democratic-csi"
	}

	allDatasetIDs := make(map[string]bool)
	for i := range allDatasets {
		allDatasetIDs[allDatasets[i].ID] = true
	}

	hasChildren := func(datasetID string) bool {
		prefix := datasetID + "/"
		for id := range allDatasetIDs {
			if strings.HasPrefix(id, prefix) {
				return true
			}
		}
		return false
	}

	var volumes []UnmanagedVolume
	for i := range allDatasets {
		ds := &allDatasets[i]

		if ds.ID == searchPath {
			continue
		}
		if managedIDs[ds.ID] {
			continue
		}
		if !showAll && isSystemDataset(ds.ID, searchPath) {
			continue
		}

		vol := UnmanagedVolume{
			Dataset:     ds.ID,
			Name:        extractDatasetName(ds.ID),
			Type:        ds.Type,
			IsContainer: hasChildren(ds.ID),
		}

		if ds.Used != nil {
			if val, ok := ds.Used["parsed"].(float64); ok {
				vol.SizeBytes = int64(val)
				vol.Size = FormatBytes(vol.SizeBytes)
			}
		}

		if share, ok := nfsShareByPath[ds.Mountpoint]; ok {
			vol.Protocol = protocolNFS
			vol.NFSShareID = share.ID
			vol.NFSSharePath = share.Path
		} else if ds.Type == datasetTypeVolume {
			vol.Protocol = "block"
		}

		if manager, ok := democraticIDs[ds.ID]; ok {
			vol.ManagedBy = manager
		}

		volumes = append(volumes, vol)
	}

	return volumes, nil
}

// GetVolumeDetails retrieves detailed information about a volume.
//
//nolint:gocyclo // complexity from protocol and property extraction is acceptable
func GetVolumeDetails(ctx context.Context, client tnsapi.ClientInterface, volumeRef string) (*VolumeDetails, error) {
	var dataset *tnsapi.DatasetWithProperties

	ds, err := client.FindDatasetByCSIVolumeName(ctx, "", volumeRef)
	if err == nil && ds != nil {
		dataset = ds
	} else {
		datasets, findErr := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
		if findErr != nil {
			return nil, fmt.Errorf("failed to query datasets: %w", findErr)
		}
		for i := range datasets {
			if datasets[i].ID == volumeRef {
				dataset = &datasets[i]
				break
			}
		}
	}

	if dataset == nil {
		return nil, fmt.Errorf("%w: %s", errVolumeNotFound, volumeRef)
	}

	details := &VolumeDetails{
		Dataset:    dataset.ID,
		Type:       dataset.Type,
		Properties: make(map[string]string),
	}

	if dataset.Mountpoint != "" {
		details.MountPath = dataset.Mountpoint
	}
	if dataset.Used != nil {
		if val, ok := dataset.Used["parsed"].(float64); ok {
			details.UsedBytes = int64(val)
			details.UsedHuman = FormatBytes(details.UsedBytes)
		}
	}

	for key, prop := range dataset.UserProperties {
		details.Properties[key] = prop.Value

		switch key {
		case tnsapi.PropertyCSIVolumeName:
			details.VolumeID = prop.Value
		case tnsapi.PropertyProtocol:
			details.Protocol = prop.Value
		case tnsapi.PropertyCapacityBytes:
			details.CapacityBytes = tnsapi.StringToInt64(prop.Value)
			details.CapacityHuman = FormatBytes(details.CapacityBytes)
		case tnsapi.PropertyCreatedAt:
			details.CreatedAt = prop.Value
		case tnsapi.PropertyDeleteStrategy:
			details.DeleteStrategy = prop.Value
		case tnsapi.PropertyAdoptable:
			details.Adoptable = prop.Value == valueTrue
		case tnsapi.PropertyContentSourceType:
			details.ContentSourceType = prop.Value
		case tnsapi.PropertyContentSourceID:
			details.ContentSourceID = prop.Value
		case tnsapi.PropertyCloneMode:
			details.CloneMode = prop.Value
		case tnsapi.PropertyOriginSnapshot:
			details.OriginSnapshot = prop.Value
		}
	}

	switch details.Protocol {
	case protocolNFS:
		if shareDetails, shareErr := getNFSShareDetails(ctx, client, dataset); shareErr == nil {
			details.NFSShare = shareDetails
		}
	case protocolNVMeOF:
		if subsysDetails, subsysErr := getNVMeOFSubsystemDetails(ctx, client, dataset); subsysErr == nil {
			details.NVMeOFSubsystem = subsysDetails
		}
	case protocolSMB:
		if smbDetails, smbErr := getSMBShareDetails(ctx, client, dataset); smbErr == nil {
			details.SMBShare = smbDetails
		}
	case protocolISCSI:
		if iscsiDetails, iscsiErr := getISCSITargetDetails(ctx, client, dataset); iscsiErr == nil {
			details.ISCSITarget = iscsiDetails
		}
	}

	return details, nil
}

func getNFSShareDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*NFSShareDetails, error) {
	sharePath := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if dataset.Mountpoint != "" {
		sharePath = dataset.Mountpoint
	}
	if sharePath == "" {
		return nil, errNoSharePath
	}

	shares, err := client.QueryNFSShare(ctx, sharePath)
	if err != nil {
		return nil, err
	}
	if len(shares) == 0 {
		return nil, fmt.Errorf("%w for path %s", errNoNFSShare, sharePath)
	}

	share := shares[0]
	return &NFSShareDetails{
		ID:      share.ID,
		Path:    share.Path,
		Hosts:   share.Hosts,
		Enabled: share.Enabled,
	}, nil
}

func getNVMeOFSubsystemDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*NVMeOFSubsystemDetails, error) {
	nqn := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}
	if nqn == "" {
		return nil, errNoSubsystemNQN
	}

	subsystem, err := client.NVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return nil, err
	}

	return &NVMeOFSubsystemDetails{
		ID:      subsystem.ID,
		Name:    subsystem.Name,
		NQN:     subsystem.NQN,
		Serial:  subsystem.Serial,
		Enabled: subsystem.Enabled,
	}, nil
}

func getSMBShareDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*SMBShareDetails, error) {
	if prop, ok := dataset.UserProperties[tnsapi.PropertySMBShareID]; ok && prop.Value != "" {
		shareID, err := strconv.Atoi(prop.Value)
		if err == nil && shareID > 0 {
			share, shareErr := client.QuerySMBShareByID(ctx, shareID)
			if shareErr != nil {
				return nil, shareErr
			}
			return &SMBShareDetails{
				ID:      share.ID,
				Name:    share.Name,
				Path:    share.Path,
				Enabled: share.Enabled,
			}, nil
		}
	}

	sharePath := ""
	if dataset.Mountpoint != "" {
		sharePath = dataset.Mountpoint
	}
	if sharePath == "" {
		return nil, errNoSharePath
	}

	shares, err := client.QuerySMBShare(ctx, sharePath)
	if err != nil || len(shares) == 0 {
		return nil, fmt.Errorf("%w for path %s", errNoSMBShare, sharePath)
	}

	share := shares[0]
	return &SMBShareDetails{
		ID:      share.ID,
		Name:    share.Name,
		Path:    share.Path,
		Enabled: share.Enabled,
	}, nil
}

func getISCSITargetDetails(ctx context.Context, client tnsapi.ClientInterface, dataset *tnsapi.DatasetWithProperties) (*ISCSITargetDetails, error) {
	iqn := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyISCSIIQN]; ok {
		iqn = prop.Value
	}
	if iqn == "" {
		return nil, errNoISCSIIQN
	}

	targetName := ""
	if prop, ok := dataset.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
		targetName = prop.Value
	}

	target, err := client.ISCSITargetByName(ctx, targetName)
	if err != nil {
		return nil, err
	}

	return &ISCSITargetDetails{
		ID:   target.ID,
		Name: target.Name,
		IQN:  iqn,
	}, nil
}

func isSystemDataset(datasetID, searchPath string) bool {
	relPath := strings.TrimPrefix(datasetID, searchPath+"/")
	systemPrefixes := []string{
		"ix-applications",
		"ix-",
		".system",
		"iocage",
	}
	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	return false
}

func extractDatasetName(datasetID string) string {
	parts := strings.Split(datasetID, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return datasetID
}

// extractVolumes extracts VolumeInfo from pre-fetched managed datasets (no API calls).
func extractVolumes(datasets []tnsapi.DatasetWithProperties) []VolumeInfo {
	var volumes []VolumeInfo
	for _, ds := range datasets {
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID == "" {
			continue
		}

		vol := VolumeInfo{
			Dataset:  ds.ID,
			VolumeID: volumeID,
			Type:     ds.Type,
		}

		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			vol.Protocol = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyCapacityBytes]; ok {
			vol.CapacityBytes = tnsapi.StringToInt64(prop.Value)
			vol.CapacityHuman = FormatBytes(vol.CapacityBytes)
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyDeleteStrategy]; ok {
			vol.DeleteStrategy = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyAdoptable]; ok {
			vol.Adoptable = prop.Value == valueTrue
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyContentSourceType]; ok {
			vol.ContentSourceType = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyContentSourceID]; ok {
			vol.ContentSourceID = prop.Value
		}

		volumes = append(volumes, vol)
	}
	return volumes
}

// extractClones extracts CloneInfo from pre-fetched managed datasets (no API calls).
func extractClones(datasets []tnsapi.DatasetWithProperties) []CloneInfo {
	var clones []CloneInfo
	for _, ds := range datasets {
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		sourceTypeProp, hasSourceType := ds.UserProperties[tnsapi.PropertyContentSourceType]
		if !hasSourceType || sourceTypeProp.Value == "" {
			continue
		}

		clone := CloneInfo{
			Dataset:    ds.ID,
			SourceType: sourceTypeProp.Value,
		}

		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			clone.VolumeID = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			clone.Protocol = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyContentSourceID]; ok {
			clone.SourceID = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyCloneMode]; ok {
			clone.CloneMode = prop.Value
		} else {
			clone.CloneMode = tnsapi.CloneModeCOW
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyOriginSnapshot]; ok {
			clone.OriginSnapshot = prop.Value
		}

		switch clone.CloneMode {
		case tnsapi.CloneModeCOW:
			clone.DependencyNote = "Source snapshot CANNOT be deleted"
		case tnsapi.CloneModePromoted:
			clone.DependencyNote = "Source snapshot CAN be deleted"
		case tnsapi.CloneModeDetached:
			clone.DependencyNote = "Fully independent (no dependencies)"
		default:
			clone.DependencyNote = "Unknown mode"
		}

		clones = append(clones, clone)
	}
	return clones
}

// extractDetachedSnapshots extracts SnapshotInfo from pre-fetched detached datasets (no API calls).
func extractDetachedSnapshots(detachedDatasets []tnsapi.DatasetWithProperties) []SnapshotInfo {
	var snapshots []SnapshotInfo
	for _, ds := range detachedDatasets {
		if prop, ok := ds.UserProperties[tnsapi.PropertyManagedBy]; !ok || prop.Value != tnsapi.ManagedByValue {
			continue
		}

		snap := SnapshotInfo{
			Type: "detached",
		}

		if prop, ok := ds.UserProperties[tnsapi.PropertySnapshotID]; ok {
			snap.Name = prop.Value
		} else {
			parts := strings.Split(ds.ID, "/")
			snap.Name = parts[len(parts)-1]
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertySourceVolumeID]; ok {
			snap.SourceVolume = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertySourceDataset]; ok {
			snap.SourceDataset = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			snap.Protocol = prop.Value
		}
		if prop, ok := ds.UserProperties[tnsapi.PropertyDeleteStrategy]; ok {
			snap.DeleteStrategy = prop.Value
		}

		snapshots = append(snapshots, snap)
	}
	return snapshots
}

// FormatBytes converts bytes to human-readable format.
func FormatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1fTi", float64(bytes)/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.1fGi", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1fMi", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1fKi", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
