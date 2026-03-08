package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
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
	data := s.fetchAllData(ctx)
	data.Version = s.version

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		klog.Errorf("Template error: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (s *Server) fetchAllData(ctx context.Context) Data {
	data := Data{}

	// Phase 1: Concurrent raw data fetch — all API calls run in parallel.
	g, gctx := errgroup.WithContext(ctx)

	var managedDatasets []tnsapi.DatasetWithProperties
	var detachedDatasets []tnsapi.DatasetWithProperties
	var allSnapshots []tnsapi.Snapshot
	var nfsShares []tnsapi.NFSShare
	var smbShares []tnsapi.SMBShare
	var nvmeSubsystems []tnsapi.NVMeOFSubsystem
	var iscsiTargets []tnsapi.ISCSITarget
	var allDatasets []tnsapi.Dataset
	var democraticDatasets []tnsapi.DatasetWithProperties
	var k8sData *K8sEnrichmentResult

	g.Go(func() error {
		var err error
		managedDatasets, err = s.client.FindDatasetsByProperty(gctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
		return err
	})

	g.Go(func() error {
		var err error
		detachedDatasets, err = s.client.FindDatasetsByProperty(gctx, "", tnsapi.PropertyDetachedSnapshot, valueTrue)
		return err
	})

	g.Go(func() error {
		var err error
		allSnapshots, err = s.client.QuerySnapshots(gctx, []interface{}{})
		return err
	})

	g.Go(func() error {
		var err error
		nfsShares, err = s.client.QueryAllNFSShares(gctx, "")
		return err
	})

	g.Go(func() error {
		var err error
		smbShares, err = s.client.QueryAllSMBShares(gctx, "")
		return err
	})

	g.Go(func() error {
		var err error
		nvmeSubsystems, err = s.client.ListAllNVMeOFSubsystems(gctx)
		return err
	})

	g.Go(func() error {
		var err error
		iscsiTargets, err = s.client.QueryISCSITargets(gctx, nil)
		return err
	})

	if s.pool != "" {
		g.Go(func() error {
			var err error
			allDatasets, err = s.client.QueryAllDatasets(gctx, s.pool)
			return err
		})

		g.Go(func() error {
			//nolint:errcheck // non-fatal if democratic-csi datasets not found
			democraticDatasets, _ = s.client.FindDatasetsByProperty(gctx, s.pool, "democratic-csi:csi_share_volume_context", "")
			return nil
		})
	}

	g.Go(func() error {
		result := EnrichWithK8sData(gctx, false)
		k8sData = result
		return nil
	})

	if err := g.Wait(); err != nil {
		klog.Warningf("Failed to fetch dashboard data: %v", err)
		return data
	}

	// Phase 2: In-memory processing — no API calls.
	data.Volumes = extractVolumes(managedDatasets)
	data.Clones = extractClones(managedDatasets)

	data.Snapshots = matchSnapshotsToDatasets(allSnapshots, managedDatasets)
	data.Snapshots = append(data.Snapshots, extractDetachedSnapshots(detachedDatasets)...)

	if s.pool != "" {
		data.Unmanaged = buildUnmanagedFromData(allDatasets, managedDatasets, nfsShares, democraticDatasets, s.pool)
	}

	// Health annotation from pre-fetched maps
	resources := BuildHealthMapsFromData(nfsShares, smbShares, nvmeSubsystems, iscsiTargets)
	AnnotateHealthFromMaps(data.Volumes, managedDatasets, resources)

	// K8s binding enrichment
	if k8sData != nil && k8sData.Available {
		for i := range data.Volumes {
			if binding := MatchK8sBinding(k8sData.Bindings, data.Volumes[i].Dataset, data.Volumes[i].VolumeID); binding != nil {
				data.Volumes[i].K8s = binding
			}
		}
	}

	data.Summary = CalculateSummary(data.Volumes, data.Snapshots, data.Clones)

	return data
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
	data := s.fetchAllData(ctx)
	writeJSONResponse(w, data.Summary)
}

func (s *Server) handlePartialVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "volumes_table.html", volumes); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialSnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snapshots, err := FindManagedSnapshots(ctx, s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "snapshots_table.html", snapshots); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialClones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clones, err := FindClonedVolumes(ctx, s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "clones_table.html", clones); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := s.fetchAllData(ctx)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "summary_cards.html", data.Summary); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *Server) handlePartialUnmanaged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "unmanaged_table.html", unmanaged); err != nil {
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
