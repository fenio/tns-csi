// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	helmReleaseName = "tns-csi-driver"
	helmNamespace   = "kube-system"
)

// ErrUnknownProtocol is returned when an unknown protocol is specified.
var ErrUnknownProtocol = errors.New("unknown protocol")

// HelmDeployer handles Helm-based deployment of the CSI driver.
type HelmDeployer struct {
	config *Config
}

// NewHelmDeployer creates a new HelmDeployer.
func NewHelmDeployer(config *Config) *HelmDeployer {
	return &HelmDeployer{config: config}
}

// getChartPath returns the absolute path to the Helm chart.
func getChartPath() (string, error) {
	// Get the git repo root
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get repo root: %w", err)
	}
	repoRoot := strings.TrimSpace(string(output))
	return filepath.Join(repoRoot, "charts", "tns-csi-driver"), nil
}

// Deploy installs or upgrades the CSI driver using Helm.
func (h *HelmDeployer) Deploy(protocol string) error {
	chartPath, err := getChartPath()
	if err != nil {
		return fmt.Errorf("failed to get chart path: %w", err)
	}

	args := []string{
		"upgrade", "--install",
		helmReleaseName,
		chartPath,
		"--namespace", helmNamespace,
		"--create-namespace",
		"--wait",
		"--timeout", "5m",
		"--set", "truenas.url=wss://" + h.config.TrueNASHost + "/api/current",
		"--set", "truenas.apiKey=" + h.config.TrueNASAPIKey,
		"--set", "truenas.pool=" + h.config.TrueNASPool,
		"--set", "image.repository=" + h.config.CSIImageRepo,
		"--set", "image.tag=" + h.config.CSIImageTag,
		"--set", "truenas.skipTLSVerify=true",
	}

	// Enable snapshots for all protocols (required for snapshot tests)
	args = append(args,
		"--set", "snapshots.enabled=true",
	)

	// Enable protocol-specific storage class
	switch protocol {
	case "nfs":
		args = append(args,
			"--set", "storageClasses.nfs.enabled=true",
			"--set", "storageClasses.nfs.name=tns-csi-nfs",
			"--set", "storageClasses.nfs.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.nfs.server="+h.config.TrueNASHost,
			"--set", "storageClasses.nvmeof.enabled=false",
			"--set", "storageClasses.iscsi.enabled=false",
		)
	case "nvmeof":
		args = append(args,
			"--set", "storageClasses.nfs.enabled=false",
			"--set", "storageClasses.nvmeof.enabled=true",
			"--set", "storageClasses.nvmeof.name=tns-csi-nvmeof",
			"--set", "storageClasses.nvmeof.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.nvmeof.server="+h.config.TrueNASHost,
			"--set", "storageClasses.nvmeof.transport=tcp",
			"--set", "storageClasses.nvmeof.port=4420",
			"--set", "storageClasses.iscsi.enabled=false",
		)
	case "iscsi":
		args = append(args,
			"--set", "storageClasses.nfs.enabled=false",
			"--set", "storageClasses.nvmeof.enabled=false",
			"--set", "storageClasses.iscsi.enabled=true",
			"--set", "storageClasses.iscsi.name=tns-csi-iscsi",
			"--set", "storageClasses.iscsi.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.iscsi.server="+h.config.TrueNASHost,
			"--set", "storageClasses.iscsi.port=3260",
		)
	case "both", "all":
		args = append(args,
			"--set", "storageClasses.nfs.enabled=true",
			"--set", "storageClasses.nfs.name=tns-csi-nfs",
			"--set", "storageClasses.nfs.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.nfs.server="+h.config.TrueNASHost,
			"--set", "storageClasses.nvmeof.enabled=true",
			"--set", "storageClasses.nvmeof.name=tns-csi-nvmeof",
			"--set", "storageClasses.nvmeof.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.nvmeof.server="+h.config.TrueNASHost,
			"--set", "storageClasses.nvmeof.transport=tcp",
			"--set", "storageClasses.nvmeof.port=4420",
			"--set", "storageClasses.iscsi.enabled=true",
			"--set", "storageClasses.iscsi.name=tns-csi-iscsi",
			"--set", "storageClasses.iscsi.pool="+h.config.TrueNASPool,
			"--set", "storageClasses.iscsi.server="+h.config.TrueNASHost,
			"--set", "storageClasses.iscsi.port=3260",
		)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownProtocol, protocol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	return h.runHelm(ctx, args...)
}

// Undeploy removes the CSI driver using Helm.
func (h *HelmDeployer) Undeploy() error {
	args := []string{
		"uninstall",
		helmReleaseName,
		"--namespace", helmNamespace,
		"--wait",
		"--timeout", "2m",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	err := h.runHelm(ctx, args...)
	if err != nil && strings.Contains(err.Error(), "not found") {
		// Release doesn't exist, that's fine
		return nil
	}
	return err
}

// IsDeployed checks if the CSI driver is currently deployed.
func (h *HelmDeployer) IsDeployed() bool {
	args := []string{
		"status",
		helmReleaseName,
		"--namespace", helmNamespace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return h.runHelm(ctx, args...) == nil
}

// WaitForReady waits for the CSI driver pods to be ready.
// This is handled by --wait in Deploy, but can be called separately if needed.
func (h *HelmDeployer) WaitForReady(timeout time.Duration) error {
	// Wait for controller deployment
	// Deployment name is: <release-name>-controller = tns-csi-driver-controller
	if err := h.waitForDeployment("tns-csi-driver-controller", timeout); err != nil {
		return fmt.Errorf("controller not ready: %w", err)
	}

	// Wait for node daemonset
	// DaemonSet name is: <release-name>-node = tns-csi-driver-node
	if err := h.waitForDaemonSet("tns-csi-driver-node", timeout); err != nil {
		return fmt.Errorf("node daemonset not ready: %w", err)
	}

	return nil
}

// waitForDeployment waits for a deployment to be available.
func (h *HelmDeployer) waitForDeployment(name string, timeout time.Duration) error {
	args := []string{
		"wait", "--for=condition=available",
		"deployment/" + name,
		"--namespace", helmNamespace,
		"--timeout", timeout.String(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
	defer cancel()
	return runKubectl(ctx, args...)
}

// waitForDaemonSet waits for a daemonset to have all pods ready.
func (h *HelmDeployer) waitForDaemonSet(name string, timeout time.Duration) error {
	// kubectl wait doesn't work well with daemonsets, so we use rollout status
	args := []string{
		"rollout", "status",
		"daemonset/" + name,
		"--namespace", helmNamespace,
		"--timeout", timeout.String(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
	defer cancel()
	return runKubectl(ctx, args...)
}

// runHelm executes a helm command.
func (h *HelmDeployer) runHelm(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("helm %s failed: %w\nstdout: %s\nstderr: %s",
			args[0], err, stdout.String(), stderr.String())
	}
	return nil
}

// runKubectl executes a kubectl command.
func runKubectl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("kubectl %s failed: %w\nstdout: %s\nstderr: %s",
			args[0], err, stdout.String(), stderr.String())
	}
	return nil
}
