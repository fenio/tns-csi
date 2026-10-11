// Package main implements the TrueNAS CSI driver entry point.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

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
	endpoint                  = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/tns.csi.io/csi.sock", "CSI endpoint")
	nodeID                    = flag.String("node-id", "", "Node ID")
	driverName                = flag.String("driver-name", "tns.csi.io", "Name of the driver")
	apiURL                    = flag.String("api-url", "", "Storage system API URL (e.g., ws://10.10.20.100/api/v2.0/websocket)")
	apiKey                    = flag.String("api-key", "", "Storage system API key (prefer the "+truenasKeyEnvName+" environment variable: flag values are visible in the process list)")
	metricsAddr               = flag.String("metrics-addr", "", "Address to expose Prometheus metrics")
	skipTLSVerify             = flag.Bool("skip-tls-verify", false, "Skip TLS certificate verification (for self-signed certificates)")
	showVersion               = flag.Bool("show-version", false, "Show version and exit")
	debug                     = flag.Bool("debug", false, "Enable debug logging (equivalent to -v=4)")
	enableNVMeDiscovery       = flag.Bool("enable-nvme-discovery", false, "Run nvme discover before nvme connect (default: false, all connection params are known from volume context)")
	maxConcurrentNVMeConnects = flag.Int("max-concurrent-nvme-connects", 5, "Maximum number of concurrent NVMe-oF connect operations per node (limits kernel NVMe subsystem lock contention)")
	dashboardAddr             = flag.String("dashboard-addr", "", "Address for in-cluster web dashboard (e.g., ':2137', empty = disabled)")
	dashboardPool             = flag.String("dashboard-pool", "", "ZFS pool for unmanaged volume discovery in dashboard")
	clusterID                 = flag.String("cluster-id", "", "Unique identifier for this cluster (for multi-cluster TrueNAS sharing)")
	maxResponseSizeMB         = flag.Int("max-response-size-mb", 10, "Maximum size in MiB of a single TrueNAS API response (WebSocket message); larger responses fail the call")
)

// truenasKeyEnvName holds the TrueNAS API key. Reading it from the environment keeps the
// secret out of the process command line (visible to every user on the node via ps
// and /proc/<pid>/cmdline), which passing --api-key=$(TNS_API_KEY) does not.
const truenasKeyEnvName = "TNS_API_KEY"

// resolveAPIKey returns the API key from --api-key if set (backward compatibility),
// otherwise from the environment, with surrounding whitespace from secrets trimmed.
func resolveAPIKey(flagValue string, getenv func(string) string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(getenv(truenasKeyEnvName))
}

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

	resolvedAPIKey := resolveAPIKey(*apiKey, os.Getenv)
	if resolvedAPIKey == "" {
		klog.Fatalf("Storage API key must be provided via the %s environment variable or --api-key", truenasKeyEnvName)
	}

	// Set version info for metrics endpoint
	metrics.SetVersionInfo(version, gitCommit, buildDate)

	klog.Infof("Starting TNS CSI Driver %s (commit: %s, built: %s)", version, gitCommit, buildDate)
	klog.V(4).Infof("Driver: %s", *driverName)
	klog.V(4).Infof("Node ID: %s", *nodeID)

	drv, err := driver.NewDriver(driver.Config{
		DriverName:                *driverName,
		Version:                   version,
		NodeID:                    *nodeID,
		Endpoint:                  *endpoint,
		APIURL:                    *apiURL,
		APIKey:                    resolvedAPIKey,
		MetricsAddr:               *metricsAddr,
		SkipTLSVerify:             *skipTLSVerify,
		EnableNVMeDiscovery:       *enableNVMeDiscovery,
		MaxConcurrentNVMeConnects: *maxConcurrentNVMeConnects,
		DashboardAddr:             *dashboardAddr,
		DashboardPool:             *dashboardPool,
		ClusterID:                 *clusterID,
		MaxResponseSizeMB:         *maxResponseSizeMB,
	})
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	if err := drv.Run(); err != nil {
		klog.Fatalf("Failed to run driver: %v", err)
	}
}
