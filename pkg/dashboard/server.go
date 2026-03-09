package dashboard

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"k8s.io/klog/v2"
)

//go:embed templates/*.html
var templateFS embed.FS

// Server holds the in-cluster dashboard server state.
type Server struct {
	client    tnsapi.ClientInterface
	templates *template.Template
	httpSrv   *http.Server
	pool      string
	version   string
}

// NewServer creates a new dashboard server.
func NewServer(client tnsapi.ClientInterface, pool, version string) (*Server, error) {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		client:    client,
		templates: tmpl,
		pool:      pool,
		version:   version,
	}, nil
}

// RegisterRoutes registers dashboard routes on an existing mux with a path prefix.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/dashboard/", s.handleDashboard)
	mux.HandleFunc("/dashboard/api/volumes", s.handleAPIVolumes)
	mux.HandleFunc("/dashboard/api/volumes/", s.handleAPIVolumeDetail)
	mux.HandleFunc("/dashboard/api/snapshots", s.handleAPISnapshots)
	mux.HandleFunc("/dashboard/api/clones", s.handleAPIClones)
	mux.HandleFunc("/dashboard/api/summary", s.handleAPISummary)
	mux.HandleFunc("/dashboard/api/unmanaged", s.handleAPIUnmanaged)
	mux.HandleFunc("/dashboard/api/metrics", s.handleAPIMetrics)
	mux.HandleFunc("/dashboard/api/metrics/raw", s.handleAPIMetricsRaw)
	mux.HandleFunc("/dashboard/partials/volumes", s.handlePartialVolumes)
	mux.HandleFunc("/dashboard/partials/snapshots", s.handlePartialSnapshots)
	mux.HandleFunc("/dashboard/partials/clones", s.handlePartialClones)
	mux.HandleFunc("/dashboard/partials/unmanaged", s.handlePartialUnmanaged)
	mux.HandleFunc("/dashboard/partials/summary", s.handlePartialSummary)
	mux.HandleFunc("/dashboard/partials/volume-detail/", s.handlePartialVolumeDetail)
	mux.HandleFunc("/dashboard/partials/metrics", s.handlePartialMetrics)
}

// Start starts the dashboard server on the specified address.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	klog.Infof("Starting dashboard server on %s", addr)
	if err := s.httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the dashboard server.
func (s *Server) Stop() {
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			klog.Errorf("Error shutting down dashboard server: %v", err)
		}
	}
}
