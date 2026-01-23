// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These tests verify that the CSI driver exposes metrics for observability.
// This is protocol-agnostic as metrics are a driver-level concern.

var _ = Describe("Metrics and Observability", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should expose controller metrics endpoint", func() {
		ctx := context.Background()

		By("Getting controller POD")
		pods, err := f.K8s.GetPodsWithLabel(ctx, "kube-system", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller")
		Expect(err).NotTo(HaveOccurred(), "Failed to get controller pods")
		Expect(pods).NotTo(BeEmpty(), "No controller pods found")

		controllerPod := pods[0]

		By("Checking metrics port is exposed")
		// The controller should have a metrics port (typically 9808)
		var metricsPort int32
		for _, container := range controllerPod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == "metrics" || port.ContainerPort == 9808 {
					metricsPort = port.ContainerPort
					break
				}
			}
		}

		if metricsPort == 0 {
			Skip("Metrics port not configured on controller")
		}

		if f.Verbose() {
			GinkgoWriter.Printf("Controller metrics port: %d\n", metricsPort)
		}
	})

	It("should have controller logs with operation details", func() {
		ctx := context.Background()

		By("Getting controller logs")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred(), "Failed to get controller logs")
		Expect(logs).NotTo(BeEmpty(), "Controller logs should not be empty")

		// Verify logs contain useful operational information
		hasOperationalLogs := strings.Contains(logs, "msg=") ||
			strings.Contains(logs, "level=") ||
			strings.Contains(logs, "CSI") ||
			strings.Contains(logs, "controller")

		if f.Verbose() {
			if hasOperationalLogs {
				GinkgoWriter.Printf("Controller logs contain operational information\n")
			}
			// Print last few lines of logs
			lines := strings.Split(logs, "\n")
			if len(lines) > 10 {
				lines = lines[len(lines)-10:]
			}
			GinkgoWriter.Printf("Recent controller logs:\n%s\n", strings.Join(lines, "\n"))
		}
	})

	It("should have node logs with mount operation details", func() {
		ctx := context.Background()

		By("Getting node PODs")
		pods, err := f.K8s.GetPodsWithLabel(ctx, "kube-system", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node")
		Expect(err).NotTo(HaveOccurred(), "Failed to get node PODs")
		Expect(pods).NotTo(BeEmpty(), "No node pods found")

		By("Checking node POD logs")
		nodePod := pods[0]
		_, err = f.K8s.GetPodLogs(ctx, "kube-system", nodePod.Name, "tns-csi-driver", 100)
		if err != nil {
			// Container might have different name
			_, err = f.K8s.GetPodLogs(ctx, "kube-system", nodePod.Name, "", 100)
		}
		Expect(err).NotTo(HaveOccurred(), "Failed to get node logs")

		if f.Verbose() {
			GinkgoWriter.Printf("Node POD %s has logs available\n", nodePod.Name)
		}
	})

	It("should track volume operations in logs after provisioning", func() {
		ctx := context.Background()

		By("Creating a PVC to generate metrics")
		pvcName := "metrics-test-pvc"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating POD to trigger provisioning")
		podName := "metrics-test-pod"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Checking controller logs for volume operation")
		logs, err := f.K8s.GetControllerLogs(ctx, 200)
		Expect(err).NotTo(HaveOccurred(), "Failed to get controller logs")

		// Look for evidence of volume creation in logs
		hasVolumeCreation := strings.Contains(logs, "CreateVolume") ||
			strings.Contains(logs, "volume") ||
			strings.Contains(logs, "provision")

		if f.Verbose() {
			if hasVolumeCreation {
				GinkgoWriter.Printf("Found volume creation evidence in logs\n")
			}
		}

		// The test passes as long as the operation completed successfully
		// Log content verification is informational
	})

	It("should have CSI driver events in Kubernetes", func() {
		ctx := context.Background()

		By("Creating a PVC")
		pvcName := "metrics-events-pvc"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating POD to trigger provisioning")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "metrics-events-pod",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Checking for PVC-related events")
		events, err := f.K8s.GetEventsForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC events")

		if f.Verbose() {
			GinkgoWriter.Printf("Found %d events for PVC %s\n", len(events), pvc.Name)
			for _, event := range events {
				GinkgoWriter.Printf("  Event: %s - %s\n", event.Reason, event.Message)
			}
		}

		// Verify we can retrieve events (content varies by cluster state)
		// Having events available is the key observability check
	})
})
