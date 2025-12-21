// Package main implements the TrueNAS CSI driver entry point.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/fenio/tns-csi/pkg/driver"
	"github.com/fenio/tns-csi/pkg/metrics"
	"k8s.io/klog/v2"
)

// Build-time variables set via -ldflags.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

var (
	endpoint      = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/tns.csi.io/csi.sock", "CSI endpoint")
	nodeID        = flag.String("node-id", "", "Node ID")
	driverName    = flag.String("driver-name", "tns.csi.io", "Name of the driver")
	apiURL        = flag.String("api-url", "", "Storage system API URL (e.g., ws://10.10.20.100/api/v2.0/websocket)")
	apiKey        = flag.String("api-key", "", "Storage system API key")
	metricsAddr   = flag.String("metrics-addr", ":8080", "Address to expose Prometheus metrics")
	skipTLSVerify = flag.Bool("skip-tls-verify", false, "Skip TLS certificate verification (for self-signed certificates)")
	showVersion   = flag.Bool("show-version", false, "Show version and exit")
	debug         = flag.Bool("debug", false, "Enable debug logging (equivalent to -v=4)")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Enable debug logging if --debug flag or DEBUG_CSI env var is set
	if *debug || os.Getenv("DEBUG_CSI") == "true" || os.Getenv("DEBUG_CSI") == "1" {
		if err := flag.Set("v", "4"); err != nil {
			klog.Warningf("Failed to set verbosity level: %v", err)
		}
	}

	if *showVersion {
		fmt.Printf("%s version: %s\n", *driverName, version)
		fmt.Printf("  Git commit: %s\n", gitCommit)
		fmt.Printf("  Build date: %s\n", buildDate)
		fmt.Printf("  Go version: %s\n", runtime.Version())
		fmt.Printf("  Platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if *nodeID == "" {
		klog.Fatal("Node ID must be provided")
	}

	if *apiURL == "" {
		klog.Fatal("Storage API URL must be provided")
	}

	if *apiKey == "" {
		klog.Fatal("Storage API key must be provided")
	}

	// Set version info for metrics endpoint
	metrics.SetVersionInfo(version, gitCommit, buildDate)

	klog.Infof("Starting TNS CSI Driver %s (commit: %s, built: %s)", version, gitCommit, buildDate)
	klog.V(4).Infof("Driver: %s", *driverName)
	klog.V(4).Infof("Node ID: %s", *nodeID)

	drv, err := driver.NewDriver(driver.Config{
		DriverName:    *driverName,
		Version:       version,
		NodeID:        *nodeID,
		Endpoint:      *endpoint,
		APIURL:        *apiURL,
		APIKey:        *apiKey,
		MetricsAddr:   *metricsAddr,
		SkipTLSVerify: *skipTLSVerify,
	})
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	if err := drv.Run(); err != nil {
		klog.Fatalf("Failed to run driver: %v", err)
	}
}
