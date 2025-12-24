// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

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
// It creates a unique namespace, deploys the CSI driver, and waits for it to be ready.
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

	// Create Helm deployer
	f.Helm = NewHelmDeployer(f.Config)

	// Deploy the CSI driver
	klog.Infof("Deploying CSI driver with protocol %s", protocol)
	if deployErr := f.Helm.Deploy(protocol); deployErr != nil {
		return fmt.Errorf("failed to deploy CSI driver: %w", deployErr)
	}

	// Register driver undeployment for cleanup (optional - may want to keep driver for debugging)
	// f.Cleanup.Add(func() error {
	// 	klog.Infof("Undeploying CSI driver")
	// 	return f.Helm.Undeploy()
	// })

	// Wait for driver to be ready
	klog.Infof("Waiting for CSI driver to be ready")
	if waitErr := f.Helm.WaitForReady(2 * time.Minute); waitErr != nil {
		return fmt.Errorf("CSI driver not ready: %w", waitErr)
	}
	klog.Infof("CSI driver is ready")

	// Create TrueNAS verifier
	truenas, err := NewTrueNASVerifier(f.Config.TrueNASHost, f.Config.TrueNASAPIKey)
	if err != nil {
		klog.Warningf("Failed to create TrueNAS verifier: %v (TrueNAS verification will be skipped)", err)
	} else {
		f.TrueNAS = truenas
		f.Cleanup.Add(func() error {
			f.TrueNAS.Close()
			return nil
		})
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

	// Register cleanup
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		klog.Infof("Cleaning up PVC %s", opts.Name)
		return f.K8s.DeletePVC(context.Background(), opts.Name)
	})

	return pvc, nil
}

// CreatePod creates a Pod and registers it for cleanup.
func (f *Framework) CreatePod(ctx context.Context, opts PodOptions) (*corev1.Pod, error) {
	pod, err := f.K8s.CreatePod(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Register cleanup
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		klog.Infof("Cleaning up Pod %s", opts.Name)
		return f.K8s.DeletePod(context.Background(), opts.Name)
	})

	return pod, nil
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
