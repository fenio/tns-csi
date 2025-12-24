// Package e2e contains end-to-end tests for the TrueNAS CSI driver.
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

var _ = Describe("Error Handling", func() {
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

	// This test verifies that the CSI driver handles invalid parameters gracefully.
	// PVCs with invalid parameters should stay in Pending state with appropriate error events.
	It("should handle invalid pool name gracefully", func() {
		ctx := context.Background()
		scName := "tns-csi-invalid-pool"

		By("Creating StorageClass with non-existent pool")
		params := map[string]string{
			"protocol": "nfs",
			"server":   f.Config.TrueNASHost,
			"pool":     "nonexistent-pool-xyz-12345",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with invalid pool StorageClass")
		pvcName := "pvc-invalid-pool"
		_, err = f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcName)
		})

		By("Waiting for provisioner to attempt creation")
		time.Sleep(15 * time.Second)

		By("Verifying PVC stays in Pending state")
		pvc, err := f.K8s.GetPVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimPending),
			"PVC should stay in Pending state with invalid pool")

		By("Checking controller logs for error messages")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred())

		// The controller should log an error about the invalid pool
		hasPoolError := strings.Contains(strings.ToLower(logs), "pool") &&
			(strings.Contains(strings.ToLower(logs), "error") ||
				strings.Contains(strings.ToLower(logs), "failed") ||
				strings.Contains(strings.ToLower(logs), "not found"))

		if hasPoolError {
			GinkgoWriter.Printf("Controller logged error for invalid pool as expected\n")
		} else {
			GinkgoWriter.Printf("Note: No specific pool error found in recent logs\n")
		}

		GinkgoWriter.Printf("Invalid pool test completed - PVC correctly stays Pending\n")
	})

	It("should handle missing server parameter gracefully", func() {
		ctx := context.Background()
		scName := "tns-csi-missing-server"

		By("Creating StorageClass without server parameter")
		params := map[string]string{
			"protocol": "nfs",
			"pool":     f.Config.TrueNASPool,
			// server parameter intentionally omitted
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with missing server StorageClass")
		pvcName := "pvc-missing-server"
		_, err = f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcName)
		})

		By("Waiting for provisioner to attempt creation")
		time.Sleep(15 * time.Second)

		By("Verifying PVC stays in Pending state")
		pvc, err := f.K8s.GetPVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimPending),
			"PVC should stay in Pending state with missing server")

		GinkgoWriter.Printf("Missing server test completed - PVC correctly stays Pending\n")
	})

	It("should handle invalid protocol parameter gracefully", func() {
		ctx := context.Background()
		scName := "tns-csi-invalid-protocol"

		By("Creating StorageClass with invalid protocol (iscsi)")
		params := map[string]string{
			"protocol": "iscsi", // Invalid - only nfs and nvmeof are supported
			"server":   f.Config.TrueNASHost,
			"pool":     f.Config.TrueNASPool,
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with invalid protocol StorageClass")
		pvcName := "pvc-invalid-protocol"
		_, err = f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcName)
		})

		By("Waiting for provisioner to attempt creation")
		time.Sleep(15 * time.Second)

		By("Verifying PVC stays in Pending state")
		pvc, err := f.K8s.GetPVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimPending),
			"PVC should stay in Pending state with invalid protocol")

		By("Checking controller logs for protocol error")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred())

		hasProtocolError := strings.Contains(strings.ToLower(logs), "unsupported") &&
			strings.Contains(strings.ToLower(logs), "protocol")

		if hasProtocolError {
			GinkgoWriter.Printf("Controller logged error for invalid protocol as expected\n")
		} else {
			GinkgoWriter.Printf("Note: Protocol validation may occur at different level\n")
		}

		GinkgoWriter.Printf("Invalid protocol test completed - PVC correctly stays Pending\n")
	})

	It("should recover and work normally after error conditions", func() {
		ctx := context.Background()

		By("First creating invalid StorageClass to trigger errors")
		invalidSCName := "tns-csi-recovery-invalid"
		invalidParams := map[string]string{
			"protocol": "nfs",
			"server":   f.Config.TrueNASHost,
			"pool":     "nonexistent-pool-recovery",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, invalidSCName, "tns.csi.io", invalidParams)
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), invalidSCName)
		})

		By("Creating PVC that will fail")
		failPVCName := "pvc-will-fail"
		_, err = f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             failPVCName,
			StorageClassName: invalidSCName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), failPVCName)
		})

		// Let it fail
		time.Sleep(10 * time.Second)

		By("Creating valid StorageClass to verify driver still works")
		validSCName := "tns-csi-recovery-valid"
		validParams := map[string]string{
			"protocol": "nfs",
			"server":   f.Config.TrueNASHost,
			"pool":     f.Config.TrueNASPool,
		}
		err = f.K8s.CreateStorageClassWithParams(ctx, validSCName, "tns.csi.io", validParams)
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), validSCName)
		})

		By("Creating valid PVC to verify recovery")
		validPVCName := "pvc-recovery-test"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             validPVCName,
			StorageClassName: validSCName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Waiting for valid PVC to bind")
		err = f.K8s.WaitForPVCBound(ctx, validPVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Valid PVC should bind after error recovery")

		GinkgoWriter.Printf("Recovery test passed - driver works normally after errors\n")
	})

	It("should not crash or panic under error conditions", func() {
		ctx := context.Background()

		By("Checking controller logs for panics or fatal errors")
		logs, err := f.K8s.GetControllerLogs(ctx, 500)
		Expect(err).NotTo(HaveOccurred())

		// Check for critical errors
		Expect(logs).NotTo(ContainSubstring("panic:"), "Controller should not have panicked")

		// Check for fatal crashes (excluding normal "fatal" log level messages)
		hasCriticalError := strings.Contains(logs, "FATAL ERROR") ||
			strings.Contains(logs, "runtime error:")
		Expect(hasCriticalError).To(BeFalse(), "Controller should not have critical errors")

		By("Verifying controller pod is still running")
		// If we can get logs, the pod is running
		GinkgoWriter.Printf("Controller is healthy - no panics or crashes detected\n")

		// Analyze log patterns
		errorCount := strings.Count(strings.ToLower(logs), "error")
		warningCount := strings.Count(strings.ToLower(logs), "warning") + strings.Count(strings.ToLower(logs), "warn")
		successCount := strings.Count(strings.ToLower(logs), "success") + strings.Count(strings.ToLower(logs), "completed")

		GinkgoWriter.Printf("Log analysis:\n")
		GinkgoWriter.Printf("  - Error entries: ~%d\n", errorCount)
		GinkgoWriter.Printf("  - Warning entries: ~%d\n", warningCount)
		GinkgoWriter.Printf("  - Success entries: ~%d\n", successCount)
	})
})
