package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"
)

var (
	errPoolNotConfigured = errors.New("pool not configured")
	errVolumeIDRequired  = errors.New("volume ID required")
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/" {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()

	// K8s-first: build volumes from PVs only (no TrueNAS calls).
	// All other data (snapshots, clones, unmanaged, health, summary) loads
	// via HTMX in the background after the page renders.
	data := Data{Version: s.version}
	volumes, _ := FetchK8sVolumes(ctx)
	if len(volumes) > 0 {
		data.Volumes = volumes
		data.Summary = CalculateSummary(volumes, nil, nil)
	}

	params := ParsePaginationParams(r)
	data.VolumesPage = PaginateVolumes(data.Volumes, params, "/dashboard/partials/volumes")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		klog.Errorf("Template error: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// CalculateSummary computes summary statistics from volumes, snapshots, and clones.
func CalculateSummary(volumes []VolumeInfo, snapshots []SnapshotInfo, clones []CloneInfo) SummaryData {
	summary := SummaryData{
		TotalVolumes:   len(volumes),
		TotalSnapshots: len(snapshots),
		TotalClones:    len(clones),
	}

	var totalBytes int64
	for i := range volumes {
		switch volumes[i].Protocol {
		case protocolNFS:
			summary.NFSVolumes++
		case protocolNVMeOF:
			summary.NVMeOFVolumes++
		case protocolISCSI:
			summary.ISCSIVolumes++
		case protocolSMB:
			summary.SMBVolumes++
		}
		totalBytes += volumes[i].CapacityBytes
		if volumes[i].HealthStatus != "" && volumes[i].HealthStatus != string(HealthStatusHealthy) {
			summary.UnhealthyVolumes++
		} else {
			summary.HealthyVolumes++
		}
	}

	summary.CapacityBytes = totalBytes
	summary.TotalCapacity = FormatBytes(totalBytes)

	return summary
}

func (s *Server) handleAPIVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	volumes, err := FindManagedVolumes(ctx, s.client)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSONResponse(w, volumes)
}

func (s *Server) handleAPISnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snapshots, err := FindManagedSnapshots(ctx, s.client)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSONResponse(w, snapshots)
}

func (s *Server) handleAPIClones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clones, err := FindClonedVolumes(ctx, s.client)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSONResponse(w, clones)
}

func (s *Server) handleAPISummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	g, gctx := errgroup.WithContext(ctx)

	var volumes []VolumeInfo
	var snapshots []SnapshotInfo
	var clones []CloneInfo

	g.Go(func() error {
		v, err := FindManagedVolumes(gctx, s.client)
		if err == nil {
			volumes = v
		}
		return err
	})
	g.Go(func() error {
		snaps, err := FindManagedSnapshots(gctx, s.client)
		if err == nil {
			snapshots = snaps
		}
		return err
	})
	g.Go(func() error {
		cl, err := FindClonedVolumes(gctx, s.client)
		if err == nil {
			clones = cl
		}
		return err
	})

	if err := g.Wait(); err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, CalculateSummary(volumes, snapshots, clones))
}

func (s *Server) handlePartialVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := ParsePaginationParams(r)

	volumes, err := FindManagedVolumes(ctx, s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	AnnotateVolumesWithHealth(ctx, s.client, volumes)

	k8sData := EnrichWithK8sData(ctx, false)
	if k8sData.Available {
		for i := range volumes {
			if binding := MatchK8sBinding(k8sData.Bindings, volumes[i].Dataset, volumes[i].VolumeID); binding != nil {
				volumes[i].K8s = binding
			}
		}
	}

	paginated := PaginateVolumes(volumes, params, "/dashboard/partials/volumes")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "volumes_table.html", paginated); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

//nolint:dupl // Similar structure per type — Go templates can't use generics
func (s *Server) handlePartialSnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := ParsePaginationParams(r)

	snapshots, err := FindManagedSnapshots(ctx, s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	paginated := PaginateSnapshots(snapshots, params, "/dashboard/partials/snapshots")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "snapshots_table.html", paginated); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

//nolint:dupl // Similar structure per type — Go templates can't use generics
func (s *Server) handlePartialClones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := ParsePaginationParams(r)

	clones, err := FindClonedVolumes(ctx, s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	paginated := PaginateClones(clones, params, "/dashboard/partials/clones")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "clones_table.html", paginated); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Fetch volumes, snapshots, and clones concurrently for summary computation.
	g, gctx := errgroup.WithContext(ctx)

	var volumes []VolumeInfo
	var snapshots []SnapshotInfo
	var clones []CloneInfo

	g.Go(func() error {
		v, err := FindManagedVolumes(gctx, s.client)
		if err == nil {
			volumes = v
		}
		return err
	})
	g.Go(func() error {
		snaps, err := FindManagedSnapshots(gctx, s.client)
		if err == nil {
			snapshots = snaps
		}
		return err
	})
	g.Go(func() error {
		cl, err := FindClonedVolumes(gctx, s.client)
		if err == nil {
			clones = cl
		}
		return err
	})

	if err := g.Wait(); err != nil {
		klog.Warningf("Failed to fetch summary data: %v", err)
	}

	summary := CalculateSummary(volumes, snapshots, clones)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "summary_cards.html", summary); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialUnmanaged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := ParsePaginationParams(r)

	if s.pool == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//nolint:errcheck,gosec // Best effort response
		w.Write([]byte(`<div class="empty-state">Pool not configured. Start dashboard with --dashboard-pool flag to see unmanaged volumes.</div>`))
		return
	}

	unmanaged, err := FindUnmanagedVolumes(ctx, s.client, s.pool, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	paginated := PaginateUnmanaged(unmanaged, params, "/dashboard/partials/unmanaged")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "unmanaged_table.html", paginated); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handleAPIUnmanaged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.pool == "" {
		writeJSONError(w, errPoolNotConfigured)
		return
	}

	unmanaged, err := FindUnmanagedVolumes(ctx, s.client, s.pool, false)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, unmanaged)
}

func (s *Server) handlePartialVolumeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	volumeID := strings.TrimPrefix(r.URL.Path, "/dashboard/partials/volume-detail/")
	if volumeID == "" {
		http.Error(w, "Volume ID required", http.StatusBadRequest)
		return
	}

	details, err := GetVolumeDetails(ctx, s.client, volumeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	k8sData := EnrichWithK8sData(ctx, true)
	if k8sData.Available {
		if binding := MatchK8sBinding(k8sData.Bindings, details.Dataset, details.VolumeID); binding != nil {
			details.K8s = binding
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "volume_detail.html", details); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handleAPIVolumeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	volumeID := strings.TrimPrefix(r.URL.Path, "/dashboard/api/volumes/")
	if volumeID == "" {
		writeJSONError(w, errVolumeIDRequired)
		return
	}

	details, err := GetVolumeDetails(ctx, s.client, volumeID)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, details)
}

func (s *Server) handlePartialMetrics(w http.ResponseWriter, _ *http.Request) {
	metrics := GatherLocalMetrics()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "metrics_panel.html", metrics); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handleAPIMetrics(w http.ResponseWriter, _ *http.Request) {
	metrics := GatherLocalMetrics()
	metrics.RawMetrics = ""
	writeJSONResponse(w, metrics)
}

func (s *Server) handleAPIMetricsRaw(w http.ResponseWriter, _ *http.Request) {
	rawMetrics, err := GatherRawMetrics()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	//nolint:errcheck,gosec // Best effort response
	w.Write([]byte(rawMetrics))
}

func writeJSONResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		klog.Errorf("JSON encode error: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	//nolint:errcheck,errchkjson,gosec // Best effort error response
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
