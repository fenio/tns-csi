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
	endpoint    = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/tns.csi.io/csi.sock", "CSI endpoint")
	nodeID      = flag.String("node-id", "", "Node ID")
	driverName  = flag.String("driver-name", "tns.csi.io", "Name of the driver")
	version     = flag.String("version", "v0.1.0", "Version of the driver")
	apiURL      = flag.String("api-url", "", "Storage system API URL (e.g., ws://10.10.20.100/api/v2.0/websocket)")
	apiKey      = flag.String("api-key", "", "Storage system API key")
	metricsAddr = flag.String("metrics-addr", ":8080", "Address to expose Prometheus metrics")
	showVersion = flag.Bool("show-version", false, "Show version and exit")
	debug       = flag.Bool("debug", false, "Enable debug logging (equivalent to -v=4)")
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
		fmt.Printf("%s version: %s\n", *driverName, *version)
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

	klog.Infof("Starting TNS CSI Driver %s", *version)
	klog.V(4).Infof("Driver: %s", *driverName)
	klog.V(4).Infof("Node ID: %s", *nodeID)

	drv, err := driver.NewDriver(driver.Config{
		DriverName:  *driverName,
		Version:     *version,
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
