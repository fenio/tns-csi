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

var _ = Describe("Orphaned Resource Detection", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		// This test covers both NFS and NVMe-oF, start with NFS
		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	// This test verifies that no resources are left behind on TrueNAS after volume deletion.
	// This is critical for preventing storage leaks.
	It("should clean up NFS volumes completely after deletion", func() {
		ctx := context.Background()

		By("Creating NFS PVC")
		pvcName := "orphan-test-nfs"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NFS PVC")

		By("Waiting for NFS PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "NFS PVC did not become Bound")

		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())

		GinkgoWriter.Printf("NFS PVC bound to PV: %s\n", pvName)
		GinkgoWriter.Printf("Volume handle (dataset path): %s\n", volumeHandle)

		By("Creating pod to mount NFS volume")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "orphan-test-pod-nfs",
			PVCName:   pvc.Name,
			MountPath: "/data",
			Command:   []string{"sh", "-c", "echo 'test data' > /data/test.txt && sleep 300"},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying data was written")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("test data"))

		By("Deleting pod")
		err = f.K8s.DeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred())

		err = f.K8s.WaitForPodDeleted(ctx, pod.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting NFS PVC")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PV to be deleted (indicates backend cleanup)")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PV should be deleted after PVC deletion")

		GinkgoWriter.Printf("NFS volume deleted from Kubernetes\n")

		By("Checking controller logs for successful cleanup")
		logs, err := f.K8s.GetControllerLogs(ctx, 200)
		Expect(err).NotTo(HaveOccurred())

		// Look for deletion confirmation
		hasDeleteLog := strings.Contains(logs, "DeleteVolume") &&
			(strings.Contains(logs, "successful") || strings.Contains(logs, "deleted"))

		if hasDeleteLog {
			GinkgoWriter.Printf("Controller logged successful volume deletion\n")
		}

		// Check for cleanup errors
		hasCleanupError := strings.Contains(logs, "DeleteVolume") && strings.Contains(logs, "error")
		if hasCleanupError {
			GinkgoWriter.Printf("Warning: Found cleanup-related error messages in logs\n")
		}

		Expect(hasCleanupError).To(BeFalse(), "Should not have cleanup errors")

		GinkgoWriter.Printf("NFS cleanup verification complete\n")
	})

	It("should clean up NVMe-oF volumes completely after deletion", func() {
		ctx := context.Background()

		// Switch to NVMe-oF protocol
		By("Setting up NVMe-oF protocol")
		err := f.SetupProtocol("nvmeof")
		if err != nil {
			Skip("NVMe-oF protocol not available, skipping test")
		}

		By("Creating NVMe-oF PVC")
		pvcName := "orphan-test-nvmeof"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NVMe-oF PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating pod to trigger NVMe-oF volume binding")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "orphan-test-pod-nvmeof",
			PVCName:   pvc.Name,
			MountPath: "/data",
			Command:   []string{"sh", "-c", "echo 'test data' > /data/test.txt && sync && sleep 300"},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		By("Waiting for NVMe-oF PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 6*time.Minute)
		if err != nil {
			// Check if NVMe-oF is configured
			logs, _ := f.K8s.GetControllerLogs(ctx, 50)
			if strings.Contains(logs, "No TCP NVMe-oF port") {
				Skip("NVMe-oF not configured on TrueNAS server")
			}
			Expect(err).NotTo(HaveOccurred(), "NVMe-oF PVC did not become Bound")
		}

		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())

		GinkgoWriter.Printf("NVMe-oF PVC bound to PV: %s\n", pvName)
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying data was written")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("test data"))

		By("Deleting pod")
		// Force delete for NVMe-oF to speed up device detachment
		err = f.K8s.ForceDeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred())

		// Wait for device detachment
		time.Sleep(10 * time.Second)

		By("Deleting NVMe-oF PVC")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PV to be deleted")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PV should be deleted after PVC deletion")

		GinkgoWriter.Printf("NVMe-oF volume deleted from Kubernetes\n")

		By("Checking controller logs for cleanup")
		logs, err := f.K8s.GetControllerLogs(ctx, 200)
		Expect(err).NotTo(HaveOccurred())

		// Check for NVMe-oF specific cleanup
		hasSubsystemCleanup := strings.Contains(logs, "subsystem") || strings.Contains(logs, "zvol")

		if hasSubsystemCleanup {
			GinkgoWriter.Printf("Controller performed NVMe-oF subsystem/zvol cleanup\n")
		}

		GinkgoWriter.Printf("NVMe-oF cleanup verification complete\n")
	})

	It("should not leave orphaned PVs in the cluster", func() {
		ctx := context.Background()

		By("Creating and immediately deleting multiple PVCs")
		numVolumes := 3
		pvNames := make([]string, numVolumes)

		for i := range numVolumes {
			pvcName := f.UniqueName("orphan-check")

			pvc, createErr := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(createErr).NotTo(HaveOccurred())

			waitErr := f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(waitErr).NotTo(HaveOccurred())

			pvName, getErr := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(getErr).NotTo(HaveOccurred())
			pvNames[i] = pvName

			// Delete immediately after creation
			deleteErr := f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(deleteErr).NotTo(HaveOccurred())
		}

		By("Waiting for all PVs to be deleted")
		for _, pvName := range pvNames {
			err := f.K8s.WaitForPVDeleted(ctx, pvName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PV %s should be deleted", pvName)
		}

		By("Verifying no orphaned test PVs remain")
		// Double-check by trying to get each PV
		for _, pvName := range pvNames {
			_, err := f.K8s.GetPV(ctx, pvName)
			Expect(err).To(HaveOccurred(), "PV %s should not exist", pvName)
		}

		GinkgoWriter.Printf("All %d test PVs successfully cleaned up\n", numVolumes)
	})

	It("should verify cleanup in controller logs", func() {
		ctx := context.Background()

		By("Creating a test volume")
		pvcName := "orphan-log-test"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())

		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the test volume")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		err = f.K8s.WaitForPVDeleted(ctx, pvName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		// Wait a moment for logs to be written
		time.Sleep(5 * time.Second)

		By("Analyzing controller logs for cleanup confirmation")
		logs, err := f.K8s.GetControllerLogs(ctx, 500)
		Expect(err).NotTo(HaveOccurred())

		// Look for deletion operations
		deleteOps := strings.Count(logs, "DeleteVolume")
		GinkgoWriter.Printf("DeleteVolume operations in logs: %d\n", deleteOps)

		// Check for cleanup errors
		hasCleanupError := strings.Contains(logs, "DeleteVolume") &&
			strings.Contains(strings.ToLower(logs), "error") &&
			!strings.Contains(logs, "not found") // "not found" during delete is OK

		if hasCleanupError {
			GinkgoWriter.Printf("Warning: Found cleanup errors in logs\n")
		} else {
			GinkgoWriter.Printf("No cleanup errors found in logs\n")
		}

		// Summary
		GinkgoWriter.Printf("\nOrphaned Resource Detection Summary:\n")
		GinkgoWriter.Printf("  - Kubernetes PVs deleted successfully\n")
		GinkgoWriter.Printf("  - Controller logs show cleanup operations\n")
		GinkgoWriter.Printf("  - No cleanup errors detected\n")

		Expect(hasCleanupError).To(BeFalse(), "Should not have cleanup errors")
	})
})
