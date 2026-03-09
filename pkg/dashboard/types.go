// Package dashboard provides an in-cluster web dashboard for the TNS CSI driver.
// It reuses the controller's persistent TrueNAS WebSocket connection and reads
// metrics directly from prometheus.DefaultGatherer.
package dashboard

// Data contains all data for the dashboard template.
//
//nolint:govet // field alignment not critical for this struct
type Data struct {
	Summary     SummaryData       `json:"summary"`
	Volumes     []VolumeInfo      `json:"volumes"`
	VolumesPage PaginatedVolumes  `json:"-"` // paginated view for template rendering
	Snapshots   []SnapshotInfo    `json:"snapshots"`
	Clones      []CloneInfo       `json:"clones"`
	Unmanaged   []UnmanagedVolume `json:"unmanaged"`
	Version     string            `json:"version"`
	Error       string            `json:"error,omitempty"`
}

// SummaryData contains summary statistics.
//
//nolint:govet // field alignment not critical for this struct
type SummaryData struct {
	TotalVolumes     int    `json:"totalVolumes"`
	NFSVolumes       int    `json:"nfsVolumes"`
	NVMeOFVolumes    int    `json:"nvmeofVolumes"`
	ISCSIVolumes     int    `json:"iscsiVolumes"`
	SMBVolumes       int    `json:"smbVolumes"`
	TotalSnapshots   int    `json:"totalSnapshots"`
	TotalClones      int    `json:"totalClones"`
	TotalCapacity    string `json:"totalCapacity"`
	CapacityBytes    int64  `json:"capacityBytes"`
	HealthyVolumes   int    `json:"healthyVolumes"`
	UnhealthyVolumes int    `json:"unhealthyVolumes"`
}

// VolumeInfo represents a tns-csi managed volume.
//
//nolint:govet // field alignment not critical for display struct
type VolumeInfo struct {
	Dataset           string            `json:"dataset"           yaml:"dataset"`
	VolumeID          string            `json:"volumeId"          yaml:"volumeId"`
	Protocol          string            `json:"protocol"          yaml:"protocol"`
	CapacityHuman     string            `json:"capacityHuman"     yaml:"capacityHuman"`
	DeleteStrategy    string            `json:"deleteStrategy"    yaml:"deleteStrategy"`
	Type              string            `json:"type"              yaml:"type"`
	ContentSourceType string            `json:"contentSourceType" yaml:"contentSourceType"`
	ContentSourceID   string            `json:"contentSourceId"   yaml:"contentSourceId"`
	HealthStatus      string            `json:"healthStatus"      yaml:"healthStatus"`
	HealthIssue       string            `json:"healthIssue"       yaml:"healthIssue"`
	K8s               *K8sVolumeBinding `json:"k8s,omitempty"     yaml:"k8s,omitempty"`
	CapacityBytes     int64             `json:"capacityBytes"     yaml:"capacityBytes"`
	Adoptable         bool              `json:"adoptable"         yaml:"adoptable"`
}

// SnapshotInfo represents a tns-csi managed snapshot.
type SnapshotInfo struct {
	Name           string `json:"name"           yaml:"name"`
	SourceVolume   string `json:"sourceVolume"   yaml:"sourceVolume"`
	SourceDataset  string `json:"sourceDataset"  yaml:"sourceDataset"`
	Protocol       string `json:"protocol"       yaml:"protocol"`
	Type           string `json:"type"           yaml:"type"`
	DeleteStrategy string `json:"deleteStrategy" yaml:"deleteStrategy"`
}

// CloneInfo represents a tns-csi managed cloned volume.
type CloneInfo struct {
	VolumeID       string `json:"volumeId"       yaml:"volumeId"`
	Dataset        string `json:"dataset"        yaml:"dataset"`
	Protocol       string `json:"protocol"       yaml:"protocol"`
	CloneMode      string `json:"cloneMode"      yaml:"cloneMode"`
	SourceType     string `json:"sourceType"     yaml:"sourceType"`
	SourceID       string `json:"sourceId"       yaml:"sourceId"`
	OriginSnapshot string `json:"originSnapshot" yaml:"originSnapshot"`
	DependencyNote string `json:"dependencyNote" yaml:"dependencyNote"`
}

// UnmanagedVolume represents a volume not managed by tns-csi.
//
//nolint:govet // field alignment not critical for display struct
type UnmanagedVolume struct {
	Dataset      string `json:"dataset"                yaml:"dataset"`
	Name         string `json:"name"                   yaml:"name"`
	Type         string `json:"type"                   yaml:"type"`
	IsContainer  bool   `json:"isContainer"            yaml:"isContainer"`
	Protocol     string `json:"protocol"               yaml:"protocol"`
	Size         string `json:"size"                   yaml:"size"`
	SizeBytes    int64  `json:"sizeBytes"              yaml:"sizeBytes"`
	NFSShareID   int    `json:"nfsShareId,omitempty"   yaml:"nfsShareId,omitempty"`
	NFSSharePath string `json:"nfsSharePath,omitempty" yaml:"nfsSharePath,omitempty"`
	ManagedBy    string `json:"managedBy,omitempty"    yaml:"managedBy,omitempty"`
}

// HealthStatus represents the health status of a volume.
type HealthStatus string

// HealthStatus constants for volume health reporting.
const (
	HealthStatusHealthy   HealthStatus = "Healthy"
	HealthStatusDegraded  HealthStatus = "Degraded"
	HealthStatusUnhealthy HealthStatus = "Unhealthy"
)

// VolumeHealth represents the health status of a single volume.
//
//nolint:govet // field alignment not critical for display struct
type VolumeHealth struct {
	VolumeID   string       `json:"volumeId"             yaml:"volumeId"`
	Dataset    string       `json:"dataset"              yaml:"dataset"`
	Protocol   string       `json:"protocol"             yaml:"protocol"`
	Status     HealthStatus `json:"status"               yaml:"status"`
	Issues     []string     `json:"issues"               yaml:"issues"`
	ShareOK    *bool        `json:"shareOk,omitempty"    yaml:"shareOk,omitempty"`
	SubsysOK   *bool        `json:"subsysOk,omitempty"   yaml:"subsysOk,omitempty"`
	SMBShareOK *bool        `json:"smbShareOk,omitempty" yaml:"smbShareOk,omitempty"`
	TargetOK   *bool        `json:"targetOk,omitempty"   yaml:"targetOk,omitempty"`
	DatasetOK  bool         `json:"datasetOk"            yaml:"datasetOk"`
}

// HealthReport contains the overall health report.
//
//nolint:govet // field alignment not critical for display struct
type HealthReport struct {
	Summary  HealthSummary  `json:"summary"  yaml:"summary"`
	Volumes  []VolumeHealth `json:"volumes"  yaml:"volumes"`
	Problems []VolumeHealth `json:"problems" yaml:"problems"`
}

// HealthSummary contains health summary statistics.
type HealthSummary struct {
	TotalVolumes     int `json:"totalVolumes"     yaml:"totalVolumes"`
	HealthyVolumes   int `json:"healthyVolumes"   yaml:"healthyVolumes"`
	DegradedVolumes  int `json:"degradedVolumes"  yaml:"degradedVolumes"`
	UnhealthyVolumes int `json:"unhealthyVolumes" yaml:"unhealthyVolumes"`
}

// K8sVolumeBinding holds Kubernetes PV/PVC/Pod data for a volume.
type K8sVolumeBinding struct {
	PVName       string   `json:"pvName"                 yaml:"pvName"`
	PVCName      string   `json:"pvcName,omitempty"      yaml:"pvcName,omitempty"`
	PVCNamespace string   `json:"pvcNamespace,omitempty" yaml:"pvcNamespace,omitempty"`
	PVStatus     string   `json:"pvStatus"               yaml:"pvStatus"`
	Pods         []string `json:"pods,omitempty"         yaml:"pods,omitempty"`
}

// K8sEnrichmentResult contains the results of K8s enrichment.
type K8sEnrichmentResult struct {
	Bindings  map[string]*K8sVolumeBinding // keyed by CSI volume handle
	Available bool                         // true if K8s data was successfully fetched
}

// VolumeDetails contains detailed information about a volume.
//
//nolint:govet // field alignment not critical for display struct
type VolumeDetails struct {
	Dataset           string                  `json:"dataset"                     yaml:"dataset"`
	VolumeID          string                  `json:"volumeId"                    yaml:"volumeId"`
	Protocol          string                  `json:"protocol"                    yaml:"protocol"`
	Type              string                  `json:"type"                        yaml:"type"`
	MountPath         string                  `json:"mountPath"                   yaml:"mountPath"`
	CapacityBytes     int64                   `json:"capacityBytes"               yaml:"capacityBytes"`
	CapacityHuman     string                  `json:"capacityHuman"               yaml:"capacityHuman"`
	UsedBytes         int64                   `json:"usedBytes"                   yaml:"usedBytes"`
	UsedHuman         string                  `json:"usedHuman"                   yaml:"usedHuman"`
	CreatedAt         string                  `json:"createdAt"                   yaml:"createdAt"`
	DeleteStrategy    string                  `json:"deleteStrategy"              yaml:"deleteStrategy"`
	Adoptable         bool                    `json:"adoptable"                   yaml:"adoptable"`
	ContentSourceType string                  `json:"contentSourceType,omitempty" yaml:"contentSourceType,omitempty"`
	ContentSourceID   string                  `json:"contentSourceId,omitempty"   yaml:"contentSourceId,omitempty"`
	CloneMode         string                  `json:"cloneMode,omitempty"         yaml:"cloneMode,omitempty"`
	OriginSnapshot    string                  `json:"originSnapshot,omitempty"    yaml:"originSnapshot,omitempty"`
	ZFSOrigin         string                  `json:"zfsOrigin,omitempty"         yaml:"zfsOrigin,omitempty"`
	K8s               *K8sVolumeBinding       `json:"k8s,omitempty"               yaml:"k8s,omitempty"`
	NFSShare          *NFSShareDetails        `json:"nfsShare,omitempty"          yaml:"nfsShare,omitempty"`
	NVMeOFSubsystem   *NVMeOFSubsystemDetails `json:"nvmeofSubsystem,omitempty"   yaml:"nvmeofSubsystem,omitempty"`
	SMBShare          *SMBShareDetails        `json:"smbShare,omitempty"          yaml:"smbShare,omitempty"`
	ISCSITarget       *ISCSITargetDetails     `json:"iscsiTarget,omitempty"       yaml:"iscsiTarget,omitempty"`
	Properties        map[string]string       `json:"properties"                  yaml:"properties"`
}

// NFSShareDetails contains NFS share information.
//
//nolint:govet // field alignment not critical for display struct
type NFSShareDetails struct {
	ID      int      `json:"id"      yaml:"id"`
	Path    string   `json:"path"    yaml:"path"`
	Hosts   []string `json:"hosts"   yaml:"hosts"`
	Enabled bool     `json:"enabled" yaml:"enabled"`
}

// NVMeOFSubsystemDetails contains NVMe-oF subsystem information.
//
//nolint:govet // field alignment not critical for display struct
type NVMeOFSubsystemDetails struct {
	ID      int    `json:"id"      yaml:"id"`
	Name    string `json:"name"    yaml:"name"`
	NQN     string `json:"nqn"     yaml:"nqn"`
	Serial  string `json:"serial"  yaml:"serial"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// SMBShareDetails contains SMB share information.
//
//nolint:govet // field alignment not critical for display struct
type SMBShareDetails struct {
	ID      int    `json:"id"      yaml:"id"`
	Name    string `json:"name"    yaml:"name"`
	Path    string `json:"path"    yaml:"path"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// ISCSITargetDetails contains iSCSI target information.
//
//nolint:govet // field alignment not critical for display struct
type ISCSITargetDetails struct {
	ID   int    `json:"id"   yaml:"id"`
	Name string `json:"name" yaml:"name"`
	IQN  string `json:"iqn"  yaml:"iqn"`
}

// MetricsSummary contains parsed metrics for dashboard display.
//
//nolint:govet // field alignment not critical for display struct
type MetricsSummary struct {
	WebSocketConnected     bool    `json:"websocketConnected"`
	WebSocketReconnects    int64   `json:"websocketReconnects"`
	ConnectionDurationSecs float64 `json:"connectionDurationSecs"`
	TotalOperations        int64   `json:"totalOperations"`
	SuccessOperations      int64   `json:"successOperations"`
	ErrorOperations        int64   `json:"errorOperations"`
	NFSOperations          int64   `json:"nfsOperations"`
	NVMeOFOperations       int64   `json:"nvmeofOperations"`
	ISCSIOperations        int64   `json:"iscsiOperations"`
	SMBOperations          int64   `json:"smbOperations"`
	CreateOperations       int64   `json:"createOperations"`
	DeleteOperations       int64   `json:"deleteOperations"`
	ExpandOperations       int64   `json:"expandOperations"`
	MessagesSent           int64   `json:"messagesSent"`
	MessagesReceived       int64   `json:"messagesReceived"`
	RawMetrics             string  `json:"rawMetrics,omitempty"`
	Error                  string  `json:"error,omitempty"`
}

// PaginationParams holds parsed query parameters for pagination/search/sort.
type PaginationParams struct {
	Query    string
	Sort     string
	Order    string // "asc" or "desc"
	Page     int
	PageSize int
}

// PaginatedVolumes wraps a page of volumes with pagination metadata.
//
//nolint:govet // field alignment not critical for display struct
type PaginatedVolumes struct {
	Items      []VolumeInfo
	Page       int
	PageSize   int
	TotalItems int
	TotalPages int
	Query      string
	Sort       string
	Order      string
	BaseURL    string
	HasPrev    bool
	HasNext    bool
}

// PaginatedSnapshots wraps a page of snapshots with pagination metadata.
//
//nolint:govet // field alignment not critical for display struct
type PaginatedSnapshots struct {
	Items      []SnapshotInfo
	Page       int
	PageSize   int
	TotalItems int
	TotalPages int
	Query      string
	Sort       string
	Order      string
	BaseURL    string
	HasPrev    bool
	HasNext    bool
}

// PaginatedClones wraps a page of clones with pagination metadata.
//
//nolint:govet // field alignment not critical for display struct
type PaginatedClones struct {
	Items      []CloneInfo
	Page       int
	PageSize   int
	TotalItems int
	TotalPages int
	Query      string
	Sort       string
	Order      string
	BaseURL    string
	HasPrev    bool
	HasNext    bool
}

// PaginatedUnmanaged wraps a page of unmanaged volumes with pagination metadata.
//
//nolint:govet // field alignment not critical for display struct
type PaginatedUnmanaged struct {
	Items      []UnmanagedVolume
	Page       int
	PageSize   int
	TotalItems int
	TotalPages int
	Query      string
	Sort       string
	Order      string
	BaseURL    string
	HasPrev    bool
	HasNext    bool
}

// Protocol constants.
const (
	protocolNFS    = "nfs"
	protocolNVMeOF = "nvmeof"
	protocolISCSI  = "iscsi"
	protocolSMB    = "smb"
)

const (
	valueTrue         = "true"
	datasetTypeVolume = "VOLUME"
)
