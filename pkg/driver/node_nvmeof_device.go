package driver

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/fenio/tns-csi/pkg/retry"
	"k8s.io/klog/v2"
)

// connectNVMeOFTarget discovers and connects to an NVMe-oF target with retry logic.
// This handles transient failures when TrueNAS has just created a new subsystem
// (e.g., for snapshot-restored volumes) but it's not yet fully ready for connections.
func (s *NodeService) connectNVMeOFTarget(ctx context.Context, params *nvmeOFConnectionParams) error {
	if s.enableDiscovery {
		// Discover the NVMe-oF target
		klog.V(4).Infof("Discovering NVMe-oF target at %s:%s", params.server, params.port)
		discoverCtx, discoverCancel := context.WithTimeout(ctx, 15*time.Second)
		defer discoverCancel()
		discoverCmd := exec.CommandContext(discoverCtx, "nvme", "discover", "-t", params.transport, "-a", params.server, "-s", params.port)
		if output, discoverErr := discoverCmd.CombinedOutput(); discoverErr != nil {
			klog.Warningf("NVMe discover failed (this may be OK if target is already known): %v, output: %s", discoverErr, string(output))
		}
	} else {
		klog.V(4).Infof("Skipping NVMe discover for %s (all connection params known from volume context)", params.nqn)
	}

	// Connect to the NVMe-oF target with retry logic
	// This is necessary because newly created subsystems (e.g., from snapshot restore)
	// may not be immediately ready for connections on TrueNAS
	klog.V(4).Infof("Connecting to NVMe-oF target: %s", params.nqn)

	config := retry.Config{
		MaxAttempts:       6,               // Up to 6 attempts
		InitialBackoff:    2 * time.Second, // Start with 2s delay
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 1.5,
		RetryableFunc:     isRetryableNVMeConnectError,
		OperationName:     fmt.Sprintf("nvme-connect(%s)", params.nqn),
	}

	if err := retry.WithRetryNoResult(ctx, config, func() error {
		return s.attemptNVMeConnect(ctx, params)
	}); err != nil {
		return err
	}

	// After successful connection, give the kernel time to register the controller
	// and enumerate namespaces. This initial delay helps prevent the race condition
	// where we look for the device before the kernel has finished setting it up.
	const postConnectDelay = 2 * time.Second
	klog.V(4).Infof("Waiting %v for kernel to register NVMe controller and namespaces", postConnectDelay)
	time.Sleep(postConnectDelay)

	// Trigger udev to process new NVMe devices
	triggerUdevForNVMeSubsystem(ctx)

	return nil
}

// attemptNVMeConnect performs a single NVMe connect attempt.
func (s *NodeService) attemptNVMeConnect(ctx context.Context, params *nvmeOFConnectionParams) error {
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connectCancel()

	// NVMe-oF connection with resilience and performance options:
	// --reconnect-delay=2: Wait 2 seconds before reconnecting after connection loss
	// --ctrl-loss-tmo=60: Keep retrying for 60 seconds before giving up
	// --keep-alive-tmo=5: Send keepalive every 5 seconds to detect dead connections
	// --nr-io-queues: Number of I/O queues (default 4; configurable via StorageClass)
	// --queue-size: Queue depth per I/O queue (kernel default 127; configurable via StorageClass)
	connectArgs := []string{
		"connect",
		"-t", params.transport,
		"-n", params.nqn,
		"-a", params.server,
		"-s", params.port,
		"--reconnect-delay=2",
		"--ctrl-loss-tmo=60",
		"--keep-alive-tmo=5",
	}

	if params.nrIOQueues != "" {
		connectArgs = append(connectArgs, "--nr-io-queues="+params.nrIOQueues)
		klog.V(4).Infof("Using custom nr-io-queues=%s for NVMe-oF connection", params.nrIOQueues)
	} else {
		connectArgs = append(connectArgs, "--nr-io-queues=4") // default
	}

	if params.queueSize != "" {
		connectArgs = append(connectArgs, "--queue-size="+params.queueSize)
		klog.V(4).Infof("Using custom queue-size=%s for NVMe-oF connection", params.queueSize)
	}

	connectCmd := exec.CommandContext(connectCtx, "nvme", connectArgs...)
	output, err := connectCmd.CombinedOutput()
	if err != nil {
		// Check if already connected (this is success, not an error)
		if strings.Contains(string(output), "already connected") {
			klog.V(4).Infof("NVMe device already connected (output: %s)", string(output))
			return nil
		}
		return fmt.Errorf("nvme connect failed: %w, output: %s", err, string(output))
	}

	return nil
}

// isRetryableNVMeConnectError determines if an NVMe connect error is transient
// and should be retried. This includes errors from newly created subsystems
// that aren't fully initialized on TrueNAS yet.
func isRetryableNVMeConnectError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// These errors indicate the subsystem isn't ready yet (transient)
	retryablePatterns := []string{
		"failed to write to nvme-fabrics device", // Subsystem not yet accepting connections
		"could not add new controller",           // Controller registration pending
		"connection refused",                     // Target not listening yet
		"connection timed out",                   // Target slow to respond
		"No route to host",                       // Network path not ready
		"Host is down",                           // Target initializing
		"Network is unreachable",                 // Transient network issue
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// checkNVMeCLI checks if nvme-cli is installed.
func (s *NodeService) checkNVMeCLI(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "nvme", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrNVMeCLINotFound, err)
	}
	return nil
}

// disconnectNVMeOF disconnects from an NVMe-oF target and waits for device cleanup.
func (s *NodeService) disconnectNVMeOF(ctx context.Context, nqn string) error {
	klog.V(4).Infof("Disconnecting from NVMe-oF target: %s", nqn)

	disconnectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(disconnectCtx, "nvme", "disconnect", "-n", nqn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already disconnected
		if strings.Contains(string(output), "No subsystems") || strings.Contains(string(output), "not found") {
			klog.V(4).Infof("NVMe device already disconnected")
			return nil
		}
		return fmt.Errorf("failed to disconnect NVMe-oF device: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully disconnected from NVMe-oF target")

	// Wait for kernel to cleanup device nodes
	const deviceCleanupDelay = 1 * time.Second
	klog.V(4).Infof("Waiting %v for kernel to cleanup NVMe devices after disconnect", deviceCleanupDelay)
	select {
	case <-time.After(deviceCleanupDelay):
		klog.V(4).Infof("Device cleanup delay complete")
	case <-ctx.Done():
		klog.Warningf("Context canceled during device cleanup delay: %v", ctx.Err())
		return ctx.Err()
	}

	return nil
}

// rescanNVMeNamespace rescans an NVMe namespace to ensure the kernel has fresh device data.
func (s *NodeService) rescanNVMeNamespace(ctx context.Context, devicePath string) error {
	// Extract controller path from device path (e.g., /dev/nvme0n1 -> /dev/nvme0)
	controllerPath := extractNVMeController(devicePath)
	if controllerPath == "" {
		return fmt.Errorf("%w: %s", ErrNVMeControllerNotFound, devicePath)
	}

	klog.V(4).Infof("Rescanning NVMe namespace on controller %s (device: %s)", controllerPath, devicePath)

	rescanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme ns-rescan failed for %s: %v, output: %s (this may be OK)", controllerPath, err, string(output))
		return fmt.Errorf("ns-rescan failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Successfully rescanned NVMe namespace on controller %s", controllerPath)
	return nil
}

// extractNVMeController extracts the controller device path from a namespace device path
// (e.g., /dev/nvme0n1 -> /dev/nvme0, /dev/nvme1n2 -> /dev/nvme1).
func extractNVMeController(devicePath string) string {
	// Find the position of 'n' followed by a digit (the namespace part)
	for i := len(devicePath) - 1; i >= 0; i-- {
		if devicePath[i] == 'n' && i > 0 && devicePath[i-1] >= '0' && devicePath[i-1] <= '9' {
			if i+1 < len(devicePath) && devicePath[i+1] >= '0' && devicePath[i+1] <= '9' {
				return devicePath[:i]
			}
		}
	}
	return ""
}
