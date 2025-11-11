// Package main implements the TrueNAS CSI driver entry point.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fenio/tns-csi/pkg/driver"
	"k8s.io/klog/v2"
)

var (
	// Version information (set via build flags)
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"

	endpoint    = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/tns.csi.io/csi.sock", "CSI endpoint")
	nodeID      = flag.String("node-id", "", "Node ID")
	driverName  = flag.String("driver-name", "tns.csi.io", "Name of the driver")
	apiURL      = flag.String("api-url", "", "Storage system API URL (e.g., ws://10.10.20.100/api/v2.0/websocket)")
	apiKey      = flag.String("api-key", "", "Storage system API key")
	metricsAddr = flag.String("metrics-addr", ":8080", "Address to expose Prometheus metrics")
	showVersion = flag.Bool("version", false, "Show version information and exit")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *showVersion {
		fmt.Printf("TNS CSI Driver\n")
		fmt.Printf("  Version:    %s\n", Version)
		fmt.Printf("  Git Commit: %s\n", GitCommit)
		fmt.Printf("  Build Date: %s\n", BuildDate)
		fmt.Printf("  Driver:     %s\n", *driverName)
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

	klog.Infof("Starting TNS CSI Driver")
	klog.Infof("Driver: %s", *driverName)
	klog.Infof("Version: %s (commit: %s, built: %s)", Version, GitCommit, BuildDate)
	klog.Infof("Node ID: %s", *nodeID)

	drv, err := driver.NewDriver(driver.Config{
		DriverName:  *driverName,
		Version:     Version,
		NodeID:      *nodeID,
		Endpoint:    *endpoint,
		APIURL:      *apiURL,
		APIKey:      *apiKey,
		MetricsAddr: *metricsAddr,
	})
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	if err := drv.Run(); err != nil {
		klog.Fatalf("Failed to run driver: %v", err)
	}
}
