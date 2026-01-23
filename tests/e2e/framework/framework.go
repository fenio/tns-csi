// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// suiteState holds suite-level state for Helm deployment.
// This allows us to deploy Helm once per suite instead of per test.
type suiteState struct {
	helm         *HelmDeployer
	config       *Config
	truenas      *TrueNASVerifier
	truenasError error
	protocol     string
	mu           sync.Mutex
	deployed     bool
}

var suite = &suiteState{}

// getDriverVersionInfo extracts version info from controller logs.
// Returns a formatted string like "v0.1.0 (commit: abc1234, built: 2024-01-22T12:00:00Z)"
// or empty string if version info cannot be extracted.
func getDriverVersionInfo() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get controller logs to find the startup message with version info
	args := []string{
		"logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller",
		"--tail=50",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		klog.V(4).Infof("Failed to get controller logs for version info: %v", err)
		return ""
	}

	// Look for the startup log line: "Starting TNS CSI Driver v0.1.0 (commit: abc1234, built: 2024-01-22T12:00:00Z)"
	logs := stdout.String()
	re := regexp.MustCompile(`Starting TNS CSI Driver (\S+) \(commit: ([^,]+), built: ([^)]+)\)`)
	matches := re.FindStringSubmatch(logs)
	if len(matches) >= 4 {
		return fmt.Sprintf("%s (commit: %s, built: %s)", matches[1], matches[2], matches[3])
	}

	// Fallback: try to find just version
	reSimple := regexp.MustCompile(`Starting TNS CSI Driver (\S+)`)
	matchesSimple := reSimple.FindStringSubmatch(logs)
	if len(matchesSimple) >= 2 {
		return matchesSimple[1]
	}

	// Last resort: check image tag from deployment
	argsImage := []string{
		"get", "deployment",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller",
		"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}",
	}
	cmdImage := exec.CommandContext(ctx, "kubectl", argsImage...)
	imageOutput, err := cmdImage.Output()
	if err == nil && len(imageOutput) > 0 {
		image := strings.TrimSpace(string(imageOutput))
		// Extract tag from image (e.g., "repo/name:tag" -> "tag")
		if idx := strings.LastIndex(image, ":"); idx != -1 {
			return "image " + image[idx+1:]
		}
		return "image " + image
	}

	return ""
}

// SetupSuite initializes the suite-level resources (Helm deployment).
// This should be called from BeforeSuite in each test suite.
func SetupSuite(protocol string) error {
	suite.mu.Lock()
	defer suite.mu.Unlock()

	if suite.deployed && suite.protocol == protocol {
		klog.Infof("Suite already set up for protocol %s, skipping Helm deployment", protocol)
		return nil
	}

	klog.Infof("Setting up suite for protocol %s", protocol)

	// Load config
	config, err := NewConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	suite.config = config

	// Create Helm deployer
	suite.helm = NewHelmDeployer(config)

	// Deploy the CSI driver
	klog.Infof("Deploying CSI driver with protocol %s", protocol)
	if deployErr := suite.helm.Deploy(protocol); deployErr != nil {
		return fmt.Errorf("failed to deploy CSI driver: %w", deployErr)
	}

	// Wait for driver to be ready
	klog.Infof("Waiting for CSI driver to be ready")
	if waitErr := suite.helm.WaitForReady(2 * time.Minute); waitErr != nil {
		return fmt.Errorf("CSI driver not ready: %w", waitErr)
	}
	klog.Infof("CSI driver is ready")

	// Log driver version info
	if versionInfo := getDriverVersionInfo(); versionInfo != "" {
		klog.Infof("Driver version: %s", versionInfo)
	}

	// Create TrueNAS verifier (store any error for later)
	truenas, truenasErr := NewTrueNASVerifier(config.TrueNASHost, config.TrueNASAPIKey)
	if truenasErr != nil {
		klog.Warningf("Failed to create TrueNAS verifier: %v (TrueNAS verification will be skipped)", truenasErr)
		suite.truenasError = truenasErr
	} else {
		suite.truenas = truenas
	}

	suite.deployed = true
	suite.protocol = protocol

	klog.Infof("Suite setup complete for protocol %s", protocol)
	return nil
}

// TeardownSuite cleans up suite-level resources.
// This should be called from AfterSuite in each test suite.
func TeardownSuite() {
	suite.mu.Lock()
	defer suite.mu.Unlock()

	if suite.truenas != nil {
		suite.truenas.Close()
		suite.truenas = nil
	}

	// Note: We don't undeploy the Helm chart here because:
	// 1. It's useful for debugging if tests fail
	// 2. The next suite will just upgrade it anyway
	// 3. CI cleanup handles final cleanup

	suite.deployed = false
	suite.protocol = ""
	klog.Infof("Suite teardown complete")
}

// Framework provides a unified interface for E2E testing.
type Framework struct {
	Config   *Config
	K8s      *KubernetesClient
	Helm     *HelmDeployer
	TrueNAS  *TrueNASVerifier
	Cleanup  *CleanupTracker
	protocol string
}

// NewFramework creates a new test framework with configuration from environment.
func NewFramework() (*Framework, error) {
	config, err := NewConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return &Framework{
		Config:  config,
		Cleanup: NewCleanupTracker(),
	}, nil
}

// Setup initializes the framework for testing.
// It creates a unique namespace and sets up the K8s client.
// Helm deployment is handled at suite level via SetupSuite.
func (f *Framework) Setup(protocol string) error {
	f.protocol = protocol
	ctx := context.Background()

	// Generate unique namespace for this test run
	namespace := fmt.Sprintf("e2e-test-%d", time.Now().UnixNano())

	klog.Infof("Setting up E2E framework for protocol %s in namespace %s", protocol, namespace)

	// Create Kubernetes client
	k8s, err := NewKubernetesClient(f.Config.Kubeconfig, namespace)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	f.K8s = k8s

	// Create namespace
	if createErr := f.K8s.CreateNamespace(ctx); createErr != nil {
		return fmt.Errorf("failed to create namespace: %w", createErr)
	}
	klog.Infof("Created namespace %s", namespace)

	// Register namespace cleanup
	f.Cleanup.Add(func() error {
		klog.Infof("Cleaning up namespace %s", namespace)
		return f.K8s.DeleteNamespace(context.Background(), 3*time.Minute)
	})

	// Use suite-level Helm deployer if available, otherwise create one
	suite.mu.Lock()
	if suite.deployed && (suite.protocol == protocol || suite.protocol == "both" || suite.protocol == "all") {
		// Suite already deployed with compatible protocol
		f.Helm = suite.helm
		f.TrueNAS = suite.truenas
		suite.mu.Unlock()
		klog.Infof("Using suite-level Helm deployment for protocol %s", protocol)
	} else {
		suite.mu.Unlock()
		// Fallback: deploy Helm per-test (for backwards compatibility or if suite setup wasn't called)
		klog.Infof("Suite not set up, deploying Helm for protocol %s", protocol)
		f.Helm = NewHelmDeployer(f.Config)

		if deployErr := f.Helm.Deploy(protocol); deployErr != nil {
			return fmt.Errorf("failed to deploy CSI driver: %w", deployErr)
		}

		if waitErr := f.Helm.WaitForReady(2 * time.Minute); waitErr != nil {
			return fmt.Errorf("CSI driver not ready: %w", waitErr)
		}

		// Create TrueNAS verifier
		truenas, truenasErr := NewTrueNASVerifier(f.Config.TrueNASHost, f.Config.TrueNASAPIKey)
		if truenasErr != nil {
			klog.Warningf("Failed to create TrueNAS verifier: %v (TrueNAS verification will be skipped)", truenasErr)
		} else {
			f.TrueNAS = truenas
			f.Cleanup.Add(func() error {
				f.TrueNAS.Close()
				return nil
			})
		}
	}

	klog.Infof("Framework setup complete")
	return nil
}

// Teardown cleans up all resources created by the framework.
func (f *Framework) Teardown() {
	klog.Infof("Starting framework teardown")

	errors := f.Cleanup.RunAll()
	for _, err := range errors {
		klog.Errorf("Cleanup error: %v", err)
	}

	if len(errors) > 0 {
		klog.Warningf("Teardown completed with %d errors", len(errors))
	} else {
		klog.Infof("Teardown completed successfully")
	}
}

// DeferCleanup registers a cleanup function to be called during teardown.
func (f *Framework) DeferCleanup(fn CleanupFunc) {
	f.Cleanup.Add(fn)
}

// CreatePVC creates a PVC and registers it for cleanup.
func (f *Framework) CreatePVC(ctx context.Context, opts PVCOptions) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := f.K8s.CreatePVC(ctx, opts)
	if err != nil {
		return nil, err
	}

	klog.Infof("Created PVC %s (waiting for bind to get volume handle)", opts.Name)

	// Register cleanup that waits for full deletion (PVC -> PV -> CSI DeleteVolume)
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		cleanupCtx := context.Background()
		var pvName string

		// Try to get the PV name before deletion for debugging and waiting
		if boundPVC, getErr := f.K8s.GetPVC(cleanupCtx, opts.Name); getErr == nil && boundPVC.Spec.VolumeName != "" {
			pvName = boundPVC.Spec.VolumeName
			if volumeHandle, handleErr := f.K8s.GetVolumeHandle(cleanupCtx, pvName); handleErr == nil {
				klog.Infof("Cleaning up PVC %s (PV: %s, VolumeHandle: %s)", opts.Name, pvName, volumeHandle)
			} else {
				klog.Infof("Cleaning up PVC %s (PV: %s)", opts.Name, pvName)
			}
		} else {
			klog.Infof("Cleaning up PVC %s (not bound)", opts.Name)
		}

		// Delete the PVC
		if deleteErr := f.K8s.DeletePVC(cleanupCtx, opts.Name); deleteErr != nil {
			return deleteErr
		}

		// Wait for PVC to be fully deleted
		if waitErr := f.K8s.WaitForPVCDeleted(cleanupCtx, opts.Name, 4*time.Minute); waitErr != nil {
			klog.Warningf("Timeout waiting for PVC %s deletion: %v", opts.Name, waitErr)
		}

		// If we had a PV, wait for it to be deleted too (ensures CSI DeleteVolume completed)
		if pvName != "" {
			klog.Infof("Waiting for PV %s to be deleted (CSI DeleteVolume)", pvName)
			if waitErr := f.K8s.WaitForPVDeleted(cleanupCtx, pvName, 4*time.Minute); waitErr != nil {
				klog.Warningf("Timeout waiting for PV %s deletion: %v", pvName, waitErr)
			} else {
				klog.Infof("PV %s deleted successfully", pvName)
			}
		}

		return nil
	})

	return pvc, nil
}

// CreatePod creates a Pod and registers it for cleanup.
func (f *Framework) CreatePod(ctx context.Context, opts PodOptions) (*corev1.Pod, error) {
	pod, err := f.K8s.CreatePod(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Register cleanup that waits for POD to be fully deleted.
	// This is critical: if we don't wait, the PVC cleanup may run while
	// the pod is still terminating, causing EBUSY errors when the CSI driver
	// tries to unmount/delete the volume that's still in use.
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		cleanupCtx := context.Background()
		klog.Infof("Cleaning up Pod %s", opts.Name)

		if deleteErr := f.K8s.DeletePod(cleanupCtx, opts.Name); deleteErr != nil {
			return deleteErr
		}

		// Wait for POD to be fully deleted to ensure volumes are unmounted
		if waitErr := f.K8s.WaitForPodDeleted(cleanupCtx, opts.Name, 4*time.Minute); waitErr != nil {
			klog.Warningf("Timeout waiting for Pod %s deletion: %v", opts.Name, waitErr)
			// Don't return error - we still want to continue with PVC cleanup
		} else {
			klog.Infof("Pod %s deleted successfully", opts.Name)
		}

		return nil
	})

	return pod, nil
}

// RegisterPVCCleanup registers a cleanup function for a PVC that waits for full deletion.
// Use this for PVCs created via CreatePVCFromSnapshot or CreatePVCFromPVC.
func (f *Framework) RegisterPVCCleanup(pvcName string) {
	f.Cleanup.Add(func() error {
		cleanupCtx := context.Background()
		var pvName string

		// Try to get the PV name before deletion for debugging and waiting
		if boundPVC, getErr := f.K8s.GetPVC(cleanupCtx, pvcName); getErr == nil && boundPVC.Spec.VolumeName != "" {
			pvName = boundPVC.Spec.VolumeName
			if volumeHandle, handleErr := f.K8s.GetVolumeHandle(cleanupCtx, pvName); handleErr == nil {
				klog.Infof("Cleaning up PVC %s (PV: %s, VolumeHandle: %s)", pvcName, pvName, volumeHandle)
			} else {
				klog.Infof("Cleaning up PVC %s (PV: %s)", pvcName, pvName)
			}
		} else {
			klog.Infof("Cleaning up PVC %s (not bound)", pvcName)
		}

		// Delete the PVC
		if deleteErr := f.K8s.DeletePVC(cleanupCtx, pvcName); deleteErr != nil {
			return deleteErr
		}

		// Wait for PVC to be fully deleted
		if waitErr := f.K8s.WaitForPVCDeleted(cleanupCtx, pvcName, 4*time.Minute); waitErr != nil {
			klog.Warningf("Timeout waiting for PVC %s deletion: %v", pvcName, waitErr)
		}

		// If we had a PV, wait for it to be deleted too (ensures CSI DeleteVolume completed)
		if pvName != "" {
			klog.Infof("Waiting for PV %s to be deleted (CSI DeleteVolume)", pvName)
			if waitErr := f.K8s.WaitForPVDeleted(cleanupCtx, pvName, 4*time.Minute); waitErr != nil {
				klog.Warningf("Timeout waiting for PV %s deletion: %v", pvName, waitErr)
			} else {
				klog.Infof("PV %s deleted successfully", pvName)
			}
		}

		return nil
	})
}

// VerifyTrueNASCleanup verifies that a dataset was deleted from TrueNAS.
// This is useful for testing the full cleanup path.
func (f *Framework) VerifyTrueNASCleanup(ctx context.Context, datasetPath string, timeout time.Duration) error {
	if f.TrueNAS == nil {
		klog.Warningf("TrueNAS verifier not available, skipping verification for %s", datasetPath)
		return nil
	}

	return f.TrueNAS.WaitForDatasetDeleted(ctx, datasetPath, timeout)
}

// GetDatasetPathFromPV extracts the dataset path from a PV's CSI volume attributes.
func (f *Framework) GetDatasetPathFromPV(pv *corev1.PersistentVolume) string {
	if pv.Spec.CSI == nil {
		return ""
	}

	// The dataset name is stored in volumeAttributes by the CSI driver
	if datasetName, ok := pv.Spec.CSI.VolumeAttributes["datasetName"]; ok {
		return datasetName
	}

	// Fallback: try to extract from volume handle
	// Volume handle format is typically: pool/path/to/dataset
	return pv.Spec.CSI.VolumeHandle
}

// Namespace returns the test namespace.
func (f *Framework) Namespace() string {
	return f.K8s.Namespace()
}

// Protocol returns the protocol being tested.
func (f *Framework) Protocol() string {
	return f.protocol
}

// Verbose returns whether verbose output is enabled.
func (f *Framework) Verbose() bool {
	return f.Config.Verbose
}

// UniqueName generates a unique name for test resources with a given prefix.
func (f *Framework) UniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// SetupProtocol changes the protocol without doing a full setup.
// This is useful for tests that need to test multiple protocols.
func (f *Framework) SetupProtocol(protocol string) error {
	f.protocol = protocol
	klog.Infof("Switching to protocol %s", protocol)

	// Re-deploy the CSI driver with the new protocol
	if deployErr := f.Helm.Deploy(protocol); deployErr != nil {
		return fmt.Errorf("failed to deploy CSI driver for protocol %s: %w", protocol, deployErr)
	}

	// Wait for driver to be ready
	if waitErr := f.Helm.WaitForReady(2 * time.Minute); waitErr != nil {
		return fmt.Errorf("CSI driver not ready for protocol %s: %w", protocol, waitErr)
	}

	return nil
}
