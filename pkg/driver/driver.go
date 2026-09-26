package driver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

// Config contains the configuration for the driver.
type Config struct {
	DriverName                string
	Version                   string
	NodeID                    string
	Endpoint                  string
	APIURL                    string
	APIKey                    string
	MetricsAddr               string // Address to expose Prometheus metrics (e.g., ":8080")
	DashboardAddr             string // Address for in-cluster dashboard (e.g., ":9090", empty = disabled)
	DashboardPool             string // ZFS pool for unmanaged volume discovery in dashboard
	ClusterID                 string // Unique identifier for this cluster (for multi-cluster TrueNAS sharing)
	TestMode                  bool   // Enable test mode for sanity tests (skips actual mounts)
	SkipTLSVerify             bool   // Skip TLS certificate verification (for self-signed certs)
	EnableNVMeDiscovery       bool   // Run nvme discover before nvme connect (default: false)
	MaxConcurrentNVMeConnects int    // Max concurrent NVMe-oF connect operations per node (default: 5)
	MaxResponseSizeMB         int    // Max TrueNAS API response size in MiB (<= 0 = tnsapi.DefaultReadLimit)
}

// Driver is the TNS CSI driver.
type Driver struct {
	srv          *grpc.Server
	metricsSrv   *http.Server
	dashboardSrv *dashboard.Server
	apiClient    tnsapi.ClientInterface
	controller   *ControllerService
	node         *NodeService
	identity     *IdentityService
	config       Config
	testMode     bool // Test mode flag for sanity tests
}

// NewDriver creates a new driver instance.
func NewDriver(cfg Config) (*Driver, error) {
	klog.V(4).Infof("Creating new driver with config: DriverName=%s, NodeID=%s, Endpoint=%s, APIURL=%s, MetricsAddr=%s, TestMode=%v, SkipTLSVerify=%v",
		cfg.DriverName, cfg.NodeID, cfg.Endpoint, cfg.APIURL, cfg.MetricsAddr, cfg.TestMode, cfg.SkipTLSVerify)

	// Create API client
	apiClient, err := tnsapi.NewClient(cfg.APIURL, cfg.APIKey, cfg.SkipTLSVerify,
		tnsapi.WithReadLimit(int64(cfg.MaxResponseSizeMB)*1024*1024))
	if err != nil {
		return nil, err
	}

	return NewDriverWithClient(cfg, apiClient)
}

// NewDriverWithClient creates a new driver instance with a custom client.
// This is primarily used for testing with mock clients.
func NewDriverWithClient(cfg Config, client tnsapi.ClientInterface) (*Driver, error) {
	klog.V(4).Infof("Creating new driver with custom client")

	d := &Driver{
		config:    cfg,
		apiClient: client,
		testMode:  cfg.TestMode,
	}

	// Create shared node registry for both controller and node services
	nodeRegistry := NewNodeRegistry()

	// Initialize CSI services
	d.identity = NewIdentityService(cfg.DriverName, cfg.Version)
	d.controller = NewControllerService(client, nodeRegistry, cfg.ClusterID)
	d.node = NewNodeService(cfg.NodeID, client, cfg.TestMode, nodeRegistry, cfg.EnableNVMeDiscovery, cfg.MaxConcurrentNVMeConnects)

	return d, nil
}

// Run starts the CSI driver.
func (d *Driver) Run() error {
	u, err := url.Parse(d.config.Endpoint)
	if err != nil {
		return err
	}

	var addr string
	if u.Scheme == "unix" {
		addr = u.Path
		if removeErr := os.Remove(addr); removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
	} else {
		addr = u.Host
	}

	// Start metrics server if configured
	if d.config.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.Handle("/version", metrics.VersionHandler())
		d.metricsSrv = &http.Server{
			Addr:              d.config.MetricsAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			klog.Infof("Starting metrics server on %s", d.config.MetricsAddr)
			if serveErr := d.metricsSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				klog.Errorf("Metrics server error: %v", serveErr)
			}
		}()
	}

	// Start dashboard server if configured
	if d.config.DashboardAddr != "" {
		dashSrv, dashErr := dashboard.NewServer(d.apiClient, d.config.DashboardPool, d.config.Version, d.config.ClusterID)
		if dashErr != nil {
			klog.Errorf("Failed to create dashboard server: %v", dashErr)
		} else {
			d.dashboardSrv = dashSrv
			go func() {
				if serveErr := d.dashboardSrv.Start(d.config.DashboardAddr); serveErr != nil {
					klog.Errorf("Dashboard server error: %v", serveErr)
				}
			}()
		}
	}

	klog.Infof("Listening on %s://%s", u.Scheme, addr)
	//nolint:noctx // net.Listen is acceptable here - CSI driver lifecycle is managed by gRPC server
	listener, err := net.Listen(u.Scheme, addr)
	if err != nil {
		return err
	}

	// Create gRPC server with metrics interceptor
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(d.metricsInterceptor),
	}
	d.srv = grpc.NewServer(opts...)

	// Register CSI services
	csi.RegisterIdentityServer(d.srv, d.identity)
	csi.RegisterControllerServer(d.srv, d.controller)
	csi.RegisterNodeServer(d.srv, d.node)

	klog.Info("TNS CSI Driver is ready")
	return d.srv.Serve(listener)
}

// Stop stops the driver.
func (d *Driver) Stop() {
	klog.Info("Stopping TNS CSI Driver")

	// Stop dashboard server
	if d.dashboardSrv != nil {
		d.dashboardSrv.Stop()
	}

	// Stop metrics server
	if d.metricsSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.metricsSrv.Shutdown(ctx); err != nil {
			klog.Errorf("Error shutting down metrics server: %v", err)
		}
	}

	// Stop gRPC server
	if d.srv != nil {
		d.srv.GracefulStop()
	}

	// Close API client
	if d.apiClient != nil {
		d.apiClient.Close()
	}
}

// metricsInterceptor intercepts gRPC calls to record metrics and log requests.
func (d *Driver) metricsInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	methodParts := strings.Split(info.FullMethod, "/")
	method := methodParts[len(methodParts)-1]

	klog.V(3).Infof("GRPC call: %s", method)
	klog.V(5).Infof("GRPC request: %+v", req)

	// Start timing
	timer := metrics.NewOperationTimer(method)

	// Execute the handler
	resp, err := handler(ctx, req)

	// Record metrics
	if err != nil {
		klog.Errorf("GRPC error: %s returned error: %v", method, err)
		timer.ObserveError()
	} else {
		klog.V(5).Infof("GRPC response: %+v", resp)
		timer.ObserveSuccess()
	}

	return resp, err
}
