package dashboard

import (
	"context"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"k8s.io/klog/v2"
)

// healthResourceMaps holds bulk-queried resource maps for health checking.
type healthResourceMaps struct {
	nfsShareMap    map[string]*tnsapi.NFSShare
	nvmeSubsysMap  map[string]*tnsapi.NVMeOFSubsystem
	smbShareMap    map[string]*tnsapi.SMBShare
	iscsiTargetMap map[string]*tnsapi.ISCSITarget
}

// buildHealthResourceMaps queries all protocol resources in bulk for health checking.
func buildHealthResourceMaps(ctx context.Context, client tnsapi.ClientInterface) *healthResourceMaps {
	m := &healthResourceMaps{
		nfsShareMap:    make(map[string]*tnsapi.NFSShare),
		nvmeSubsysMap:  make(map[string]*tnsapi.NVMeOFSubsystem),
		smbShareMap:    make(map[string]*tnsapi.SMBShare),
		iscsiTargetMap: make(map[string]*tnsapi.ISCSITarget),
	}

	nfsShares, err := client.QueryAllNFSShares(ctx, "")
	if err == nil {
		for i := range nfsShares {
			m.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
		}
	}

	nvmeSubsystems, err := client.ListAllNVMeOFSubsystems(ctx)
	if err == nil {
		for i := range nvmeSubsystems {
			m.nvmeSubsysMap[nvmeSubsystems[i].Name] = &nvmeSubsystems[i]
			m.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
		}
	}

	smbShares, err := client.QueryAllSMBShares(ctx, "")
	if err == nil {
		for i := range smbShares {
			m.smbShareMap[smbShares[i].Path] = &smbShares[i]
		}
	}

	iscsiTargets, err := client.QueryISCSITargets(ctx, nil)
	if err == nil {
		for i := range iscsiTargets {
			m.iscsiTargetMap[iscsiTargets[i].Name] = &iscsiTargets[i]
		}
	}

	return m
}

// CheckVolumeHealth checks the health of all managed volumes.
func CheckVolumeHealth(ctx context.Context, client tnsapi.ClientInterface) (*HealthReport, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	resources := buildHealthResourceMaps(ctx, client)

	report := &HealthReport{
		Volumes:  make([]VolumeHealth, 0),
		Problems: make([]VolumeHealth, 0),
	}

	for i := range datasets {
		ds := &datasets[i]

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

		health := VolumeHealth{
			VolumeID:  volumeID,
			Dataset:   ds.ID,
			DatasetOK: true,
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			health.Protocol = prop.Value
		}

		switch health.Protocol {
		case protocolNFS:
			CheckNFSHealth(ds, resources.nfsShareMap, &health)
		case protocolNVMeOF:
			CheckNVMeOFHealth(ds, resources.nvmeSubsysMap, &health)
		case protocolSMB:
			CheckSMBHealth(ds, resources.smbShareMap, &health)
		case protocolISCSI:
			CheckISCSIHealth(ds, resources.iscsiTargetMap, &health)
		}

		if len(health.Issues) > 0 {
			health.Status = HealthStatusDegraded
			for _, issue := range health.Issues {
				issueLower := strings.ToLower(issue)
				if strings.Contains(issueLower, "not found") || strings.Contains(issueLower, "disabled") {
					health.Status = HealthStatusUnhealthy
					break
				}
			}
		}

		report.Summary.TotalVolumes++
		switch health.Status {
		case HealthStatusHealthy:
			report.Summary.HealthyVolumes++
		case HealthStatusDegraded:
			report.Summary.DegradedVolumes++
		case HealthStatusUnhealthy:
			report.Summary.UnhealthyVolumes++
		}

		report.Volumes = append(report.Volumes, health)
		if health.Status != HealthStatusHealthy {
			report.Problems = append(report.Problems, health)
		}
	}

	return report, nil
}

// CheckNFSHealth checks if the NFS share for a dataset is healthy.
func CheckNFSHealth(ds *tnsapi.DatasetWithProperties, nfsShareMap map[string]*tnsapi.NFSShare, health *VolumeHealth) {
	sharePath := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
	}

	if sharePath == "" {
		health.Issues = append(health.Issues, "NFS share path not found in properties")
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	share, exists := nfsShareMap[sharePath]
	if !exists {
		health.Issues = append(health.Issues, "NFS share not found for path "+sharePath)
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	shareOK := true
	if !share.Enabled {
		health.Issues = append(health.Issues, "NFS share is disabled")
		shareOK = false
	}
	health.ShareOK = &shareOK
}

// CheckNVMeOFHealth checks if the NVMe-oF subsystem for a dataset is healthy.
func CheckNVMeOFHealth(ds *tnsapi.DatasetWithProperties, nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem, health *VolumeHealth) {
	nqn := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

	if nqn == "" {
		health.Issues = append(health.Issues, "NVMe-oF subsystem NQN not found in properties")
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	_, exists := nvmeSubsysMap[nqn]
	if !exists {
		health.Issues = append(health.Issues, "NVMe-oF subsystem not found: "+nqn)
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	subsysOK := true
	health.SubsysOK = &subsysOK
}

// CheckSMBHealth checks if the SMB share for a dataset is healthy.
func CheckSMBHealth(ds *tnsapi.DatasetWithProperties, smbShareMap map[string]*tnsapi.SMBShare, health *VolumeHealth) {
	sharePath := ""
	if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
	}

	if sharePath == "" {
		health.Issues = append(health.Issues, "SMB share path not found")
		smbOK := false
		health.SMBShareOK = &smbOK
		return
	}

	share, exists := smbShareMap[sharePath]
	if !exists {
		health.Issues = append(health.Issues, "SMB share not found for path "+sharePath)
		smbOK := false
		health.SMBShareOK = &smbOK
		return
	}

	smbOK := true
	if !share.Enabled {
		health.Issues = append(health.Issues, "SMB share is disabled")
		smbOK = false
	}
	health.SMBShareOK = &smbOK
}

// CheckISCSIHealth checks if the iSCSI target for a dataset is healthy.
func CheckISCSIHealth(ds *tnsapi.DatasetWithProperties, iscsiTargetMap map[string]*tnsapi.ISCSITarget, health *VolumeHealth) {
	iqn := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyISCSIIQN]; ok {
		iqn = prop.Value
	}

	if iqn == "" {
		health.Issues = append(health.Issues, "iSCSI IQN not found in properties")
		targetOK := false
		health.TargetOK = &targetOK
		return
	}

	targetName := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
		targetName = prop.Value
	}

	_, exists := iscsiTargetMap[targetName]
	if !exists {
		health.Issues = append(health.Issues, "iSCSI target not found: "+targetName)
		targetOK := false
		health.TargetOK = &targetOK
		return
	}

	targetOK := true
	health.TargetOK = &targetOK
}

// BuildHealthMapsFromData builds health resource maps from pre-fetched data (no API calls).
func BuildHealthMapsFromData(
	nfsShares []tnsapi.NFSShare,
	smbShares []tnsapi.SMBShare,
	nvmeSubsystems []tnsapi.NVMeOFSubsystem,
	iscsiTargets []tnsapi.ISCSITarget,
) *healthResourceMaps {

	m := &healthResourceMaps{
		nfsShareMap:    make(map[string]*tnsapi.NFSShare, len(nfsShares)),
		nvmeSubsysMap:  make(map[string]*tnsapi.NVMeOFSubsystem, len(nvmeSubsystems)*2),
		smbShareMap:    make(map[string]*tnsapi.SMBShare, len(smbShares)),
		iscsiTargetMap: make(map[string]*tnsapi.ISCSITarget, len(iscsiTargets)),
	}

	for i := range nfsShares {
		m.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}
	for i := range nvmeSubsystems {
		m.nvmeSubsysMap[nvmeSubsystems[i].Name] = &nvmeSubsystems[i]
		m.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}
	for i := range smbShares {
		m.smbShareMap[smbShares[i].Path] = &smbShares[i]
	}
	for i := range iscsiTargets {
		m.iscsiTargetMap[iscsiTargets[i].Name] = &iscsiTargets[i]
	}

	return m
}

// AnnotateHealthFromMaps annotates volumes with health status using pre-fetched resource maps and datasets.
func AnnotateHealthFromMaps(volumes []VolumeInfo, managedDatasets []tnsapi.DatasetWithProperties, resources *healthResourceMaps) {
	// Build dataset lookup map
	datasetMap := make(map[string]*tnsapi.DatasetWithProperties, len(managedDatasets))
	for i := range managedDatasets {
		ds := &managedDatasets[i]
		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}
		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID != "" {
			datasetMap[volumeID] = ds
		}
	}

	for i := range volumes {
		ds, ok := datasetMap[volumes[i].VolumeID]
		if !ok {
			continue
		}

		health := VolumeHealth{
			VolumeID:  volumes[i].VolumeID,
			Dataset:   ds.ID,
			DatasetOK: true,
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		protocol := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			protocol = prop.Value
		}

		switch protocol {
		case protocolNFS:
			CheckNFSHealth(ds, resources.nfsShareMap, &health)
		case protocolNVMeOF:
			CheckNVMeOFHealth(ds, resources.nvmeSubsysMap, &health)
		case protocolSMB:
			CheckSMBHealth(ds, resources.smbShareMap, &health)
		case protocolISCSI:
			CheckISCSIHealth(ds, resources.iscsiTargetMap, &health)
		}

		if len(health.Issues) > 0 {
			health.Status = HealthStatusDegraded
			for _, issue := range health.Issues {
				issueLower := strings.ToLower(issue)
				if strings.Contains(issueLower, "not found") || strings.Contains(issueLower, "disabled") {
					health.Status = HealthStatusUnhealthy
					break
				}
			}
		}

		volumes[i].HealthStatus = string(health.Status)
		if len(health.Issues) > 0 {
			volumes[i].HealthIssue = health.Issues[0]
		}
	}
}

// AnnotateVolumesWithHealth runs health checks and annotates VolumeInfo slices with health status.
func AnnotateVolumesWithHealth(ctx context.Context, client tnsapi.ClientInterface, volumes []VolumeInfo) {
	healthReport, err := CheckVolumeHealth(ctx, client)
	if err != nil {
		klog.Warningf("Failed to check volume health: %v", err)
		return
	}

	healthMap := make(map[string]*VolumeHealth, len(healthReport.Volumes))
	for i := range healthReport.Volumes {
		healthMap[healthReport.Volumes[i].VolumeID] = &healthReport.Volumes[i]
	}

	for i := range volumes {
		if h, ok := healthMap[volumes[i].VolumeID]; ok {
			volumes[i].HealthStatus = string(h.Status)
			if len(h.Issues) > 0 {
				volumes[i].HealthIssue = h.Issues[0]
			}
		}
	}
}
