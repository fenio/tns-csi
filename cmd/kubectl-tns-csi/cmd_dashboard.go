package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	errPoolNotConfigured   = errors.New("pool not configured - start dashboard with --pool flag")
	errUnsupportedPlatform = errors.New("unsupported platform for opening browser")
)

// DashboardData contains all data for the dashboard template.
//
//nolint:govet // field alignment not critical for this struct
type DashboardData struct {
	Summary   SummaryData       `json:"summary"`
	Volumes   []VolumeInfo      `json:"volumes"`
	Snapshots []SnapshotInfo    `json:"snapshots"`
	Clones    []CloneInfo       `json:"clones"`
	Unmanaged []UnmanagedVolume `json:"unmanaged"`
	Version   string            `json:"version"`
	Error     string            `json:"error,omitempty"`
}

// SummaryData contains summary statistics.
//
//nolint:govet // field alignment not critical for this struct
type SummaryData struct {
	TotalVolumes     int    `json:"totalVolumes"`
	NFSVolumes       int    `json:"nfsVolumes"`
	NVMeOFVolumes    int    `json:"nvmeofVolumes"`
	ISCSIVolumes     int    `json:"iscsiVolumes"`
	TotalSnapshots   int    `json:"totalSnapshots"`
	TotalClones      int    `json:"totalClones"`
	TotalCapacity    string `json:"totalCapacity"`
	CapacityBytes    int64  `json:"capacityBytes"`
	HealthyVolumes   int    `json:"healthyVolumes"`
	UnhealthyVolumes int    `json:"unhealthyVolumes"`
}

// dashboardServer holds the server state.
type dashboardServer struct {
	cfg       *connectionConfig
	templates *template.Template
	pool      string // ZFS pool for unmanaged volume search
}

func newDashboardCmd(url, apiKey, secretRef, _ *string, skipTLSVerify *bool) *cobra.Command {
	var (
		port        int
		pool        string
		openBrowser bool
	)

	cmd := &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"serve"},
		Short:   "Start a web dashboard for tns-csi volumes",
		Long: `Start a web-based dashboard to view and manage tns-csi volumes.

The dashboard provides:
  - Volume list with status and capacity
  - Snapshot and clone information
  - Clone dependency visualization
  - Health status overview
  - Unmanaged volume discovery (requires --pool flag)

Examples:
  # Start dashboard and open in browser
  kubectl tns-csi dashboard

  # Start without opening browser
  kubectl tns-csi dashboard --open=false

  # Start on custom port
  kubectl tns-csi dashboard --port 9090

  # With pool for unmanaged volume discovery
  kubectl tns-csi dashboard --pool storage

  # With explicit credentials
  kubectl tns-csi dashboard --url wss://truenas:443/api/current --api-key KEY`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDashboard(cmd.Context(), url, apiKey, secretRef, skipTLSVerify, port, pool, openBrowser)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	cmd.Flags().StringVar(&pool, "pool", "", "ZFS pool to search for unmanaged volumes")
	cmd.Flags().BoolVar(&openBrowser, "open", true, "Open dashboard in default browser")

	return cmd
}

func runDashboard(ctx context.Context, url, apiKey, secretRef *string, skipTLSVerify *bool, port int, pool string, openBrowser bool) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return fmt.Errorf("failed to get connection config: %w", err)
	}

	// Parse templates
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("failed to parse templates: %w", err)
	}

	server := &dashboardServer{
		cfg:       cfg,
		templates: tmpl,
		pool:      pool,
	}

	// Setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handleDashboard)
	mux.HandleFunc("/api/volumes", server.handleAPIVolumes)
	mux.HandleFunc("/api/snapshots", server.handleAPISnapshots)
	mux.HandleFunc("/api/clones", server.handleAPIClones)
	mux.HandleFunc("/api/summary", server.handleAPISummary)
	mux.HandleFunc("/partials/volumes", server.handlePartialVolumes)
	mux.HandleFunc("/partials/snapshots", server.handlePartialSnapshots)
	mux.HandleFunc("/partials/clones", server.handlePartialClones)
	mux.HandleFunc("/partials/unmanaged", server.handlePartialUnmanaged)
	mux.HandleFunc("/partials/summary", server.handlePartialSummary)
	mux.HandleFunc("/partials/volume-detail/", server.handlePartialVolumeDetail)
	mux.HandleFunc("/partials/metrics", server.handlePartialMetrics)
	mux.HandleFunc("/api/unmanaged", server.handleAPIUnmanaged)
	mux.HandleFunc("/api/volumes/", server.handleAPIVolumeDetail)
	mux.HandleFunc("/api/metrics", server.handleAPIMetrics)
	mux.HandleFunc("/api/metrics/raw", server.handleAPIMetricsRaw)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Handle graceful shutdown
	done := make(chan struct{})
	//nolint:contextcheck // Signal handler intentionally uses fresh context for shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		klog.Info("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := httpServer.Shutdown(shutdownCtx); shutdownErr != nil {
			klog.Warningf("Server shutdown error: %v", shutdownErr)
		}
		close(done)
	}()

	dashboardURL := fmt.Sprintf("http://localhost:%d", port)
	fmt.Printf("TNS-CSI Dashboard starting on %s\n", dashboardURL)
	fmt.Println("Press Ctrl+C to stop")

	// Open browser if requested
	if openBrowser {
		//nolint:contextcheck // Background goroutine intentionally uses fresh context for browser open
		go func() {
			// Small delay to ensure server is ready
			time.Sleep(500 * time.Millisecond)
			fmt.Printf("Opening browser...\n")
			openCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := openURL(openCtx, dashboardURL); err != nil {
				fmt.Printf("Could not open browser automatically: %v\n", err)
			}
		}()
	}

	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}

	<-done
	return nil
}

// openURL opens the specified URL in the default browser.
func openURL(ctx context.Context, url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "cmd", "/c", "start", url)
	default:
		return errUnsupportedPlatform
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

func (s *dashboardServer) getClient(ctx context.Context) (tnsapi.ClientInterface, error) {
	return connectToTrueNAS(ctx, s.cfg)
}

func (s *dashboardServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := DashboardData{Version: version}
	ctx := r.Context()

	client, err := s.getClient(ctx)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to connect to TrueNAS: %v", err)
	} else {
		defer client.Close()
		data = s.fetchAllData(ctx, client)
		data.Version = version
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		klog.Errorf("Template error: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (s *dashboardServer) fetchAllData(ctx context.Context, client tnsapi.ClientInterface) DashboardData {
	data := DashboardData{}

	// Fetch volumes
	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		klog.Warningf("Failed to fetch volumes: %v", err)
	} else {
		data.Volumes = volumes
	}

	// Fetch snapshots
	snapshots, err := findManagedSnapshots(ctx, client)
	if err != nil {
		klog.Warningf("Failed to fetch snapshots: %v", err)
	} else {
		data.Snapshots = snapshots
	}

	// Fetch clones
	clones, err := findClonedVolumes(ctx, client)
	if err != nil {
		klog.Warningf("Failed to fetch clones: %v", err)
	} else {
		data.Clones = clones
	}

	// Fetch unmanaged volumes if pool is configured
	if s.pool != "" {
		unmanaged, err := findUnmanagedVolumes(ctx, client, s.pool, false)
		if err != nil {
			klog.Warningf("Failed to fetch unmanaged volumes: %v", err)
		} else {
			data.Unmanaged = unmanaged
		}
	}

	// Run health checks and annotate volumes
	annotateVolumesWithHealth(ctx, client, data.Volumes)

	// Calculate summary
	data.Summary = s.calculateSummary(data.Volumes, data.Snapshots, data.Clones)

	return data
}

// annotateVolumesWithHealth runs health checks and annotates VolumeInfo slices with health status.
func annotateVolumesWithHealth(ctx context.Context, client tnsapi.ClientInterface, volumes []VolumeInfo) {
	healthReport, err := checkVolumeHealth(ctx, client)
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

func (s *dashboardServer) calculateSummary(volumes []VolumeInfo, snapshots []SnapshotInfo, clones []CloneInfo) SummaryData {
	summary := SummaryData{
		TotalVolumes:   len(volumes),
		TotalSnapshots: len(snapshots),
		TotalClones:    len(clones),
	}

	var totalBytes int64
	for i := range volumes {
		switch volumes[i].Protocol {
		case "nfs":
			summary.NFSVolumes++
		case "nvmeof":
			summary.NVMeOFVolumes++
		case "iscsi":
			summary.ISCSIVolumes++
		}
		totalBytes += volumes[i].CapacityBytes
		if volumes[i].HealthStatus != "" && volumes[i].HealthStatus != string(HealthStatusHealthy) {
			summary.UnhealthyVolumes++
		} else {
			summary.HealthyVolumes++
		}
	}

	summary.CapacityBytes = totalBytes
	summary.TotalCapacity = formatBytes(totalBytes)

	return summary
}

func (s *dashboardServer) handleAPIVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, volumes)
}

func (s *dashboardServer) handleAPISnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	snapshots, err := findManagedSnapshots(ctx, client)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, snapshots)
}

func (s *dashboardServer) handleAPIClones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	clones, err := findClonedVolumes(ctx, client)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, clones)
}

func (s *dashboardServer) handleAPISummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	data := s.fetchAllData(ctx, client)
	writeJSONResponse(w, data.Summary)
}

func (s *dashboardServer) handlePartialVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Annotate with health status
	annotateVolumesWithHealth(ctx, client, volumes)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "volumes_table.html", volumes); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

//nolint:dupl // Similar structure but different data types - clearer to keep separate
func (s *dashboardServer) handlePartialSnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	snapshots, err := findManagedSnapshots(ctx, client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "snapshots_table.html", snapshots); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

//nolint:dupl // Similar structure but different data types - clearer to keep separate
func (s *dashboardServer) handlePartialClones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	clones, err := findClonedVolumes(ctx, client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "clones_table.html", clones); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *dashboardServer) handlePartialSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	data := s.fetchAllData(ctx, client)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "summary_cards.html", data.Summary); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *dashboardServer) handlePartialUnmanaged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check if pool is configured
	if s.pool == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//nolint:errcheck,gosec // Best effort response
		w.Write([]byte(`<div class="empty-state">Pool not configured. Start dashboard with --pool flag to see unmanaged volumes.</div>`))
		return
	}

	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	unmanaged, err := findUnmanagedVolumes(ctx, client, s.pool, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "unmanaged_table.html", unmanaged); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *dashboardServer) handleAPIUnmanaged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check if pool is configured
	if s.pool == "" {
		writeJSONError(w, errPoolNotConfigured)
		return
	}

	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	unmanaged, err := findUnmanagedVolumes(ctx, client, s.pool, false)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, unmanaged)
}

func (s *dashboardServer) handlePartialVolumeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract volume ID from URL path: /partials/volume-detail/{id}
	volumeID := strings.TrimPrefix(r.URL.Path, "/partials/volume-detail/")
	if volumeID == "" {
		http.Error(w, "Volume ID required", http.StatusBadRequest)
		return
	}

	client, err := s.getClient(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	details, err := getVolumeDetails(ctx, client, volumeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "volume_detail.html", details); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *dashboardServer) handleAPIVolumeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract volume ID from URL path: /api/volumes/{id}
	volumeID := strings.TrimPrefix(r.URL.Path, "/api/volumes/")
	if volumeID == "" {
		writeJSONError(w, errPoolNotConfigured) // Reuse error for consistency
		return
	}

	client, err := s.getClient(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	defer client.Close()

	details, err := getVolumeDetails(ctx, client, volumeID)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	writeJSONResponse(w, details)
}

func (s *dashboardServer) handlePartialMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	metrics, err := fetchControllerMetrics(ctx)
	if err != nil {
		metrics = &MetricsSummary{Error: err.Error()}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "metrics_panel.html", metrics); err != nil {
		klog.Errorf("Template error: %v", err)
	}
}

func (s *dashboardServer) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	metrics, err := fetchControllerMetrics(ctx)
	if err != nil {
		metrics = &MetricsSummary{Error: err.Error()}
	}
	// Don't include raw metrics in JSON response to keep it small
	metrics.RawMetrics = ""

	writeJSONResponse(w, metrics)
}

func (s *dashboardServer) handleAPIMetricsRaw(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawMetrics, err := fetchRawMetrics(ctx)
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
