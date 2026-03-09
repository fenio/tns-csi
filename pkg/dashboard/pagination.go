package dashboard

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultPageSize = 50
	minPageSize     = 10
	maxPageSize     = 200
	orderDesc       = "desc"
	orderAsc        = "asc"
	fieldProtocol   = "protocol"
)

// ParsePaginationParams extracts pagination, search, and sort parameters from a request.
func ParsePaginationParams(r *http.Request) PaginationParams {
	q := r.URL.Query()

	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}

	pageSize, err := strconv.Atoi(q.Get("pageSize"))
	if err != nil || pageSize < minPageSize {
		pageSize = defaultPageSize
	} else if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	order := strings.ToLower(q.Get("order"))
	if order != orderDesc {
		order = orderAsc
	}

	return PaginationParams{
		Page:     page,
		PageSize: pageSize,
		Query:    q.Get("q"),
		Sort:     q.Get("sort"),
		Order:    order,
	}
}

// paginationMeta computes common pagination metadata.
func paginationMeta(total int, p *PaginationParams) (start, end, totalPages int) {
	totalPages = (total + p.PageSize - 1) / p.PageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if p.Page > totalPages {
		p.Page = totalPages
	}
	start = (p.Page - 1) * p.PageSize
	end = start + p.PageSize
	if end > total {
		end = total
	}
	if start > total {
		start = total
	}
	return start, end, totalPages
}

// PaginateVolumes filters, sorts, and paginates a slice of VolumeInfo.
//
//nolint:dupl // Similar structure per type — Go templates can't use generics
func PaginateVolumes(volumes []VolumeInfo, p PaginationParams, baseURL string) PaginatedVolumes {
	if p.Query != "" {
		q := strings.ToLower(p.Query)
		filtered := volumes[:0:0]
		for i := range volumes {
			if volumeMatchesQuery(&volumes[i], q) {
				filtered = append(filtered, volumes[i])
			}
		}
		volumes = filtered
	}
	if p.Sort != "" {
		sortVolumes(volumes, p.Sort, p.Order)
	}

	total := len(volumes)
	start, end, totalPages := paginationMeta(total, &p)

	var items []VolumeInfo
	if start < total {
		items = volumes[start:end]
	}

	return PaginatedVolumes{
		Items: items, Page: p.Page, PageSize: p.PageSize,
		TotalItems: total, TotalPages: totalPages,
		Query: p.Query, Sort: p.Sort, Order: p.Order, BaseURL: baseURL,
		HasPrev: p.Page > 1, HasNext: p.Page < totalPages,
	}
}

// PaginateSnapshots filters, sorts, and paginates a slice of SnapshotInfo.
//
//nolint:dupl // Similar structure per type — Go templates can't use generics
func PaginateSnapshots(snapshots []SnapshotInfo, p PaginationParams, baseURL string) PaginatedSnapshots {
	if p.Query != "" {
		q := strings.ToLower(p.Query)
		filtered := snapshots[:0:0]
		for i := range snapshots {
			if snapshotMatchesQuery(&snapshots[i], q) {
				filtered = append(filtered, snapshots[i])
			}
		}
		snapshots = filtered
	}
	if p.Sort != "" {
		sortSnapshots(snapshots, p.Sort, p.Order)
	}

	total := len(snapshots)
	start, end, totalPages := paginationMeta(total, &p)

	var items []SnapshotInfo
	if start < total {
		items = snapshots[start:end]
	}

	return PaginatedSnapshots{
		Items: items, Page: p.Page, PageSize: p.PageSize,
		TotalItems: total, TotalPages: totalPages,
		Query: p.Query, Sort: p.Sort, Order: p.Order, BaseURL: baseURL,
		HasPrev: p.Page > 1, HasNext: p.Page < totalPages,
	}
}

// PaginateClones filters, sorts, and paginates a slice of CloneInfo.
//
//nolint:dupl // Similar structure per type — Go templates can't use generics
func PaginateClones(clones []CloneInfo, p PaginationParams, baseURL string) PaginatedClones {
	if p.Query != "" {
		q := strings.ToLower(p.Query)
		filtered := clones[:0:0]
		for i := range clones {
			if cloneMatchesQuery(&clones[i], q) {
				filtered = append(filtered, clones[i])
			}
		}
		clones = filtered
	}
	if p.Sort != "" {
		sortClones(clones, p.Sort, p.Order)
	}

	total := len(clones)
	start, end, totalPages := paginationMeta(total, &p)

	var items []CloneInfo
	if start < total {
		items = clones[start:end]
	}

	return PaginatedClones{
		Items: items, Page: p.Page, PageSize: p.PageSize,
		TotalItems: total, TotalPages: totalPages,
		Query: p.Query, Sort: p.Sort, Order: p.Order, BaseURL: baseURL,
		HasPrev: p.Page > 1, HasNext: p.Page < totalPages,
	}
}

// PaginateUnmanaged filters, sorts, and paginates a slice of UnmanagedVolume.
//
//nolint:dupl // Similar structure per type — Go templates can't use generics
func PaginateUnmanaged(volumes []UnmanagedVolume, p PaginationParams, baseURL string) PaginatedUnmanaged {
	if p.Query != "" {
		q := strings.ToLower(p.Query)
		filtered := volumes[:0:0]
		for i := range volumes {
			if unmanagedMatchesQuery(&volumes[i], q) {
				filtered = append(filtered, volumes[i])
			}
		}
		volumes = filtered
	}
	if p.Sort != "" {
		sortUnmanaged(volumes, p.Sort, p.Order)
	}

	total := len(volumes)
	start, end, totalPages := paginationMeta(total, &p)

	var items []UnmanagedVolume
	if start < total {
		items = volumes[start:end]
	}

	return PaginatedUnmanaged{
		Items: items, Page: p.Page, PageSize: p.PageSize,
		TotalItems: total, TotalPages: totalPages,
		Query: p.Query, Sort: p.Sort, Order: p.Order, BaseURL: baseURL,
		HasPrev: p.Page > 1, HasNext: p.Page < totalPages,
	}
}

// Search matchers

func volumeMatchesQuery(v *VolumeInfo, q string) bool {
	return strings.Contains(strings.ToLower(v.VolumeID), q) ||
		strings.Contains(strings.ToLower(v.Dataset), q) ||
		strings.Contains(strings.ToLower(v.Protocol), q) ||
		(v.K8s != nil && (strings.Contains(strings.ToLower(v.K8s.PVCName), q) ||
			strings.Contains(strings.ToLower(v.K8s.PVCNamespace), q)))
}

func snapshotMatchesQuery(s *SnapshotInfo, q string) bool {
	return strings.Contains(strings.ToLower(s.Name), q) ||
		strings.Contains(strings.ToLower(s.SourceVolume), q) ||
		strings.Contains(strings.ToLower(s.SourceDataset), q) ||
		strings.Contains(strings.ToLower(s.Protocol), q)
}

func cloneMatchesQuery(c *CloneInfo, q string) bool {
	return strings.Contains(strings.ToLower(c.VolumeID), q) ||
		strings.Contains(strings.ToLower(c.Dataset), q) ||
		strings.Contains(strings.ToLower(c.Protocol), q) ||
		strings.Contains(strings.ToLower(c.SourceID), q)
}

func unmanagedMatchesQuery(v *UnmanagedVolume, q string) bool {
	return strings.Contains(strings.ToLower(v.Dataset), q) ||
		strings.Contains(strings.ToLower(v.Name), q) ||
		strings.Contains(strings.ToLower(v.Protocol), q) ||
		strings.Contains(strings.ToLower(v.ManagedBy), q)
}

// Sort helpers

func sortVolumes(volumes []VolumeInfo, field, order string) {
	sort.SliceStable(volumes, func(i, j int) bool {
		var less bool
		switch field {
		case "volumeId":
			less = volumes[i].VolumeID < volumes[j].VolumeID
		case "dataset":
			less = volumes[i].Dataset < volumes[j].Dataset
		case fieldProtocol:
			less = volumes[i].Protocol < volumes[j].Protocol
		case "capacity":
			less = volumes[i].CapacityBytes < volumes[j].CapacityBytes
		case "pvc":
			less = pvcName(volumes[i].K8s) < pvcName(volumes[j].K8s)
		case "namespace":
			less = pvcNamespace(volumes[i].K8s) < pvcNamespace(volumes[j].K8s)
		case "health":
			less = volumes[i].HealthStatus < volumes[j].HealthStatus
		default:
			less = volumes[i].VolumeID < volumes[j].VolumeID
		}
		if order == orderDesc {
			return !less
		}
		return less
	})
}

func sortSnapshots(snapshots []SnapshotInfo, field, order string) {
	sort.SliceStable(snapshots, func(i, j int) bool {
		var less bool
		switch field {
		case "name":
			less = snapshots[i].Name < snapshots[j].Name
		case "sourceVolume":
			less = snapshots[i].SourceVolume < snapshots[j].SourceVolume
		case fieldProtocol:
			less = snapshots[i].Protocol < snapshots[j].Protocol
		case "type":
			less = snapshots[i].Type < snapshots[j].Type
		default:
			less = snapshots[i].Name < snapshots[j].Name
		}
		if order == orderDesc {
			return !less
		}
		return less
	})
}

func sortClones(clones []CloneInfo, field, order string) {
	sort.SliceStable(clones, func(i, j int) bool {
		var less bool
		switch field {
		case "volumeId":
			less = clones[i].VolumeID < clones[j].VolumeID
		case fieldProtocol:
			less = clones[i].Protocol < clones[j].Protocol
		case "cloneMode":
			less = clones[i].CloneMode < clones[j].CloneMode
		case "sourceId":
			less = clones[i].SourceID < clones[j].SourceID
		default:
			less = clones[i].VolumeID < clones[j].VolumeID
		}
		if order == orderDesc {
			return !less
		}
		return less
	})
}

func sortUnmanaged(volumes []UnmanagedVolume, field, order string) {
	sort.SliceStable(volumes, func(i, j int) bool {
		var less bool
		switch field {
		case "name":
			less = volumes[i].Name < volumes[j].Name
		case "dataset":
			less = volumes[i].Dataset < volumes[j].Dataset
		case "type":
			less = volumes[i].Type < volumes[j].Type
		case fieldProtocol:
			less = volumes[i].Protocol < volumes[j].Protocol
		case "size":
			less = volumes[i].SizeBytes < volumes[j].SizeBytes
		default:
			less = volumes[i].Name < volumes[j].Name
		}
		if order == orderDesc {
			return !less
		}
		return less
	})
}

func pvcName(k *K8sVolumeBinding) string {
	if k != nil {
		return k.PVCName
	}
	return ""
}

func pvcNamespace(k *K8sVolumeBinding) string {
	if k != nil {
		return k.PVCNamespace
	}
	return ""
}
