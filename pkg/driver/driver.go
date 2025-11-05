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
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

// Config contains the configuration for the driver.
type Config struct {
	DriverName  string
	Version     string
	NodeID      string
	Endpoint    string
	APIURL      string
	APIKey      string
	MetricsAddr string // Address to expose Prometheus metrics (e.g., ":8080")
}

// Driver is the TNS CSI driver.
type Driver struct {
	srv        *grpc.Server
	metricsSrv *http.Server
	apiClient  *tnsapi.Client
	controller *ControllerService
	node       *NodeService
	identity   *IdentityService
	config     Config
}

// NewDriver creates a new driver instance.
func NewDriver(cfg Config) (*Driver, error) {
	klog.V(4).Infof("Creating new driver with config: %+v", cfg)

	// Create API client
	apiClient, err := tnsapi.NewClient(cfg.APIURL, cfg.APIKey)
	if err != nil {
		return nil, err
	}

	d := &Driver{
		config:    cfg,
		apiClient: apiClient,
	}

	// Initialize CSI services
	d.identity = NewIdentityService(cfg.DriverName, cfg.Version)
	d.controller = NewControllerService(apiClient)
	d.node = NewNodeService(cfg.NodeID, apiClient)

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
