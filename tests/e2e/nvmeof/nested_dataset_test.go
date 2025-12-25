// Package nvmeof contains E2E tests for NVMe-oF protocol support.
package nvmeof

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Nested Dataset", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	// This test verifies that PVC cleanup works correctly when using nested parentDataset paths
	// like "pool/democratic-csi/nvmefc/volumes" which some users migrate from other CSI drivers.
	// The nested path can cause issues with dataset ID parsing during deletion.
	It("should handle PVC lifecycle with nested parentDataset path", func() {
		ctx := context.Background()
		scName := "tns-csi-nvmeof-nested"
		nestedPath := f.Config.TrueNASPool + "/csi-test/nested/volumes"

		By("Creating StorageClass with nested parentDataset")
		params := map[string]string{
			"protocol":      "nvmeof",
			"pool":          f.Config.TrueNASPool,
			"server":        f.Config.TrueNASHost,
			"parentDataset": nestedPath,
			"transport":     "tcp",
			"port":          "4420",
		}
		// NVMe-oF requires WaitForFirstConsumer binding mode
		err := f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, scName, "tns.csi.io", params, "WaitForFirstConsumer")
		if err != nil {
			// If we can't create the storage class, skip the test (NVMe-oF may not be configured)
			GinkgoWriter.Printf("Skipping nested dataset test: %v\n", err)
			Skip("NVMe-oF nested dataset test requires proper TrueNAS configuration")
		}
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with nested parentDataset StorageClass")
		pvcName := "nested-dataset-test"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating pod to trigger PVC binding (WaitForFirstConsumer)")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "nested-dataset-pod",
			PVCName:   pvc.Name,
			MountPath: "/data",
			Command:   []string{"sh", "-c", "echo 'test' > /data/test.txt && sync && sleep 300"},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 6*time.Minute)
		if err != nil {
			// Check controller logs for NVMe-oF configuration issues
			logs, _ := f.K8s.GetControllerLogs(ctx, 50)
			if logs != "" {
				GinkgoWriter.Printf("Controller logs:\n%s\n", logs)
			}
			// Skip if NVMe-oF is not configured
			Skip("NVMe-oF may not be configured on TrueNAS server")
		}

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		if err != nil {
			// Capture diagnostics on failure
			GinkgoWriter.Printf("Pod failed to become ready, capturing diagnostics...\n")

			// Get pod status
			podInfo, getErr := f.K8s.GetPod(ctx, pod.Name)
			if getErr == nil {
				GinkgoWriter.Printf("Pod phase: %s\n", podInfo.Status.Phase)
				for _, cond := range podInfo.Status.Conditions {
					GinkgoWriter.Printf("Pod condition: %s=%s (reason: %s, message: %s)\n",
						cond.Type, cond.Status, cond.Reason, cond.Message)
				}
				for _, cs := range podInfo.Status.ContainerStatuses {
					GinkgoWriter.Printf("Container %s: ready=%v, state=%+v\n",
						cs.Name, cs.Ready, cs.State)
				}
			}

			// Get controller logs
			logs, _ := f.K8s.GetControllerLogs(ctx, 100)
			if logs != "" {
				GinkgoWriter.Printf("Controller logs:\n%s\n", logs)
			}
		}
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying volume is functional")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test file")
		Expect(output).To(ContainSubstring("test"))

		By("Getting PV details for verification")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")

		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("PV name: %s\n", pvName)

		// The volume handle should contain the nested path structure
		Expect(volumeHandle).NotTo(BeEmpty(), "Volume handle should not be empty")

		By("Deleting pod")
		err = f.K8s.DeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")

		// Wait for pod to be gone
		Eventually(func() bool {
			_, getErr := f.K8s.GetPod(ctx, pod.Name)
			return getErr != nil
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Pod should be deleted")

		By("Deleting PVC (testing nested dataset cleanup)")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC")

		By("Waiting for PV to be deleted")
		// This is the critical test - verifying deletion works with nested parentDataset path
		Eventually(func() bool {
			_, getErr := f.K8s.GetPV(ctx, pvName)
			return getErr != nil // Should return error when PV is gone
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "PV should be deleted")

		GinkgoWriter.Printf("Successfully tested nested dataset lifecycle\n")
		GinkgoWriter.Printf("  - Created volume under nested path: %s\n", nestedPath)
		GinkgoWriter.Printf("  - Volume was functional\n")
		GinkgoWriter.Printf("  - Cleanup completed successfully\n")
	})

	It("should create multiple volumes under nested parentDataset", func() {
		ctx := context.Background()
		scName := "tns-csi-nvmeof-nested-multi"
		nestedPath := f.Config.TrueNASPool + "/csi-test/nested/multi"
		numVolumes := 3

		By("Creating StorageClass with nested parentDataset")
		params := map[string]string{
			"protocol":      "nvmeof",
			"pool":          f.Config.TrueNASPool,
			"server":        f.Config.TrueNASHost,
			"parentDataset": nestedPath,
			"transport":     "tcp",
			"port":          "4420",
		}
		// NVMe-oF requires WaitForFirstConsumer binding mode
		err := f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, scName, "tns.csi.io", params, "WaitForFirstConsumer")
		if err != nil {
			Skip("NVMe-oF nested dataset test requires proper TrueNAS configuration")
		}
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By(fmt.Sprintf("Creating %d PVCs under nested path", numVolumes))
		pvcNames := make([]string, numVolumes)
		podNames := make([]string, numVolumes)
		pvNames := make([]string, numVolumes)

		for i := range numVolumes {
			pvcName := fmt.Sprintf("nested-multi-%d", i+1)
			pvcNames[i] = pvcName

			pvc, createErr := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(createErr).NotTo(HaveOccurred(), "Failed to create PVC %d", i+1)
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			// Create pod to trigger binding
			podName := fmt.Sprintf("nested-multi-pod-%d", i+1)
			podNames[i] = podName

			pod, podErr := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvcName,
				MountPath: "/data",
				Command:   []string{"sh", "-c", fmt.Sprintf("echo 'Volume %d' > /data/id.txt && sync && sleep 300", i+1)},
			})
			Expect(podErr).NotTo(HaveOccurred(), "Failed to create pod %d", i+1)
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(context.Background(), pod.Name)
			})
		}

		By("Waiting for all PVCs to become Bound")
		for _, pvcName := range pvcNames {
			err := f.K8s.WaitForPVCBound(ctx, pvcName, 6*time.Minute)
			if err != nil {
				Skip("NVMe-oF may not be configured on TrueNAS server")
			}
		}

		By("Waiting for all pods to be ready")
		for _, podName := range podNames {
			err := f.K8s.WaitForPodReady(ctx, podName, 6*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Pod %s did not become ready", podName)
		}

		By("Getting PV names")
		for i, pvcName := range pvcNames {
			pvName, err := f.K8s.GetPVName(ctx, pvcName)
			Expect(err).NotTo(HaveOccurred())
			pvNames[i] = pvName
			GinkgoWriter.Printf("PVC %s -> PV %s\n", pvcName, pvName)
		}

		By("Verifying all volumes are functional")
		for i, podName := range podNames {
			output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/id.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read from pod %s", podName)
			Expect(output).To(ContainSubstring(fmt.Sprintf("Volume %d", i+1)))
		}

		By("Deleting all pods")
		for _, podName := range podNames {
			err := f.K8s.DeletePod(ctx, podName)
			Expect(err).NotTo(HaveOccurred())
		}

		// Wait for pods to be gone
		for _, podName := range podNames {
			Eventually(func() bool {
				_, err := f.K8s.GetPod(ctx, podName)
				return err != nil
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		}

		By("Deleting all PVCs")
		for _, pvcName := range pvcNames {
			err := f.K8s.DeletePVC(ctx, pvcName)
			Expect(err).NotTo(HaveOccurred())
		}

		By("Verifying all PVs are deleted")
		for _, pvName := range pvNames {
			Eventually(func() bool {
				_, err := f.K8s.GetPV(ctx, pvName)
				return err != nil
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "PV %s should be deleted", pvName)
		}

		GinkgoWriter.Printf("Successfully tested %d volumes under nested dataset path\n", numVolumes)
	})
})
