// Package iscsi contains E2E tests for iSCSI volumes.
package iscsi

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Concurrent Operations", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		numVolumes       = 3
		storageClassName = "tns-csi-iscsi"
		storageSize      = "1Gi"
		// iSCSI uses WaitForFirstConsumer, needs longer timeouts
		pvcTimeout = 180 * time.Second
		podTimeout = 180 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should handle concurrent PVC+Pod creation without race conditions", func() {
		// iSCSI uses WaitForFirstConsumer, so we need to create pods
		// along with PVCs to trigger binding
		pvcNames := make([]string, numVolumes)
		podNames := make([]string, numVolumes)
		for i := range numVolumes {
			pvcNames[i] = fmt.Sprintf("concurrent-pvc-iscsi-%d", i+1)
			podNames[i] = fmt.Sprintf("concurrent-pod-iscsi-%d", i+1)
		}

		By(fmt.Sprintf("Creating %d PVC+Pod pairs concurrently", numVolumes))
		var wg sync.WaitGroup
		errChan := make(chan error, numVolumes*2)

		for i := range numVolumes {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				defer GinkgoRecover()

				// Small stagger to avoid overwhelming API server
				time.Sleep(time.Duration(index*2) * time.Second)

				// Create PVC first
				_, pvcErr := f.K8s.CreatePVC(ctx, framework.PVCOptions{
					Name:             pvcNames[index],
					StorageClassName: storageClassName,
					Size:             storageSize,
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				})
				if pvcErr != nil {
					errChan <- fmt.Errorf("failed to create PVC %s: %w", pvcNames[index], pvcErr)
					return
				}

				// Create Pod to trigger binding (WaitForFirstConsumer)
				_, podErr := f.K8s.CreatePod(ctx, framework.PodOptions{
					Name:      podNames[index],
					PVCName:   pvcNames[index],
					MountPath: "/data",
				})
				if podErr != nil {
					errChan <- fmt.Errorf("failed to create Pod %s: %w", podNames[index], podErr)
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Check for creation errors
		for err := range errChan {
			Expect(err).NotTo(HaveOccurred())
		}

		// Register cleanup for all PVCs and Pods
		for i := range numVolumes {
			pvcName := pvcNames[i]
			podName := podNames[i]
			f.Cleanup.Add(func() error {
				_ = f.K8s.DeletePod(ctx, podName)
				return f.K8s.DeletePVC(ctx, pvcName)
			})
		}

		By("Waiting for all pods to be ready (triggers PVC binding)")
		for _, podName := range podNames {
			err := f.K8s.WaitForPodReady(ctx, podName, podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod %s should become ready", podName)
		}

		By("Waiting for all PVCs to become Bound")
		for _, pvcName := range pvcNames {
			err := f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred(), "PVC %s should become Bound", pvcName)
		}

		By("Verifying all PVCs are Bound")
		for _, pvcName := range pvcNames {
			pvc, err := f.K8s.GetPVC(ctx, pvcName)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound),
				fmt.Sprintf("PVC %s should be Bound", pvcName))
		}

		By("Verifying all PVs are unique")
		pvNames := make(map[string]bool)
		for _, pvcName := range pvcNames {
			pvc, err := f.K8s.GetPVC(ctx, pvcName)
			Expect(err).NotTo(HaveOccurred())
			pvName := pvc.Spec.VolumeName
			Expect(pvNames[pvName]).To(BeFalse(),
				fmt.Sprintf("PV %s is duplicated (race condition detected)", pvName))
			pvNames[pvName] = true
		}
		Expect(len(pvNames)).To(Equal(numVolumes), "Should have exactly %d unique PVs", numVolumes)

		By("Testing I/O on sample volumes (first, middle, last)")
		sampleIndices := []int{0, numVolumes / 2, numVolumes - 1}
		for _, idx := range sampleIndices {
			podName := podNames[idx]
			pvcName := pvcNames[idx]

			By(fmt.Sprintf("Testing I/O on %s (PVC: %s)", podName, pvcName))
			testData := fmt.Sprintf("Test data for volume %d", idx+1)
			_, err := f.K8s.ExecInPod(ctx, podName, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData), "I/O test should pass for %s", pvcName)
		}
	})

	It("should handle sequential pod failover on same PVC", func() {
		// iSCSI uses ReadWriteOnce, so only one pod can access at a time
		// This test verifies data persists across pod recreation
		const pvcName = "failover-pvc-iscsi"
		const originalPodName = "failover-pod-original"
		const newPodName = "failover-pod-new"
		const testData = "Data from original pod"

		By("Creating PVC")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: storageClassName,
			Size:             storageSize,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Creating original pod")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      originalPodName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for original pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, originalPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing data from original pod")
		_, err = f.K8s.ExecInPod(ctx, originalPodName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting original pod (simulating failover)")
		err = f.K8s.DeletePod(ctx, originalPodName)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for original pod to be deleted")
		err = f.K8s.WaitForPodDeleted(ctx, originalPodName, 120*time.Second)
		Expect(err).NotTo(HaveOccurred())

		// Give time for volume to detach
		time.Sleep(10 * time.Second)

		By("Creating new pod to access the same PVC")
		newPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
			Name:      newPodName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(newPod).NotTo(BeNil())

		// Register cleanup
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(ctx, newPodName)
		})

		By("Waiting for new pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, newPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying data persisted across pod failover")
		output, err := f.K8s.ExecInPod(ctx, newPodName, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData), "Data should persist across pod failover")

		By("Writing additional data from new pod")
		_, err = f.K8s.ExecInPod(ctx, newPodName, []string{
			"sh", "-c", "echo 'Data from new pod' >> /data/test.txt && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying new data was written")
		output, err = f.K8s.ExecInPod(ctx, newPodName, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("Data from new pod"))
	})
})
