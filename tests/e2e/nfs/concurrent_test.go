// Package nfs contains E2E tests for NFS volumes.
package nfs

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

var _ = Describe("NFS Concurrent Operations", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		numVolumes       = 5
		storageClassName = "tns-csi-nfs"
		storageSize      = "1Gi"
		pvcTimeout       = 300 * time.Second
		podTimeout       = 120 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should handle concurrent PVC creation without race conditions", func() {
		pvcNames := make([]string, numVolumes)
		for i := range numVolumes {
			pvcNames[i] = fmt.Sprintf("concurrent-pvc-%d", i+1)
		}

		By(fmt.Sprintf("Creating %d PVCs concurrently", numVolumes))
		var wg sync.WaitGroup
		errChan := make(chan error, numVolumes)

		for i := range numVolumes {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				defer GinkgoRecover()

				// Small stagger to avoid overwhelming API server
				time.Sleep(time.Duration(index*2) * time.Second)

				_, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
					Name:             pvcNames[index],
					StorageClassName: storageClassName,
					Size:             storageSize,
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				})
				if err != nil {
					errChan <- fmt.Errorf("failed to create PVC %s: %w", pvcNames[index], err)
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Check for creation errors
		for err := range errChan {
			Expect(err).NotTo(HaveOccurred())
		}

		// Register cleanup for all PVCs
		for _, pvcName := range pvcNames {
			name := pvcName // Capture for closure
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, name)
			})
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
			pvcName := pvcNames[idx]
			podName := fmt.Sprintf("test-pod-%d", idx+1)

			By("Creating test pod for PVC " + pvcName)
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pod).NotTo(BeNil())

			By(fmt.Sprintf("Waiting for pod %s to be ready", podName))
			err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Testing I/O on " + pvcName)
			testData := fmt.Sprintf("Test data for volume %d", idx+1)
			_, err = f.K8s.ExecInPod(ctx, podName, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData), "I/O test should pass for %s", pvcName)
		}
	})

	It("should handle concurrent pod creation on same PVC (ReadWriteMany)", func() {
		const sharedPVCName = "shared-rwx-pvc"
		const numPods = 3

		By("Creating shared ReadWriteMany PVC")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sharedPVCName,
			StorageClassName: storageClassName,
			Size:             storageSize,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, sharedPVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By(fmt.Sprintf("Creating %d pods accessing the same PVC concurrently", numPods))
		podNames := make([]string, numPods)
		var wg sync.WaitGroup
		errChan := make(chan error, numPods)

		for i := range numPods {
			podNames[i] = fmt.Sprintf("shared-pod-%d", i+1)
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				defer GinkgoRecover()

				pod, podErr := f.CreatePod(ctx, framework.PodOptions{
					Name:      podNames[index],
					PVCName:   sharedPVCName,
					MountPath: "/data",
				})
				if podErr != nil {
					errChan <- fmt.Errorf("failed to create pod %s: %w", podNames[index], podErr)
					return
				}
				if pod == nil {
					errChan <- fmt.Errorf("pod %s is nil", podNames[index])
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Check for creation errors
		for err := range errChan {
			Expect(err).NotTo(HaveOccurred())
		}

		By("Waiting for all pods to be ready")
		for _, podName := range podNames {
			err := f.K8s.WaitForPodReady(ctx, podName, podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod %s should become ready", podName)
		}

		By("Testing concurrent writes from different pods")
		var writeWg sync.WaitGroup
		for i, podName := range podNames {
			writeWg.Add(1)
			go func(index int, name string) {
				defer writeWg.Done()
				defer GinkgoRecover()

				fileName := fmt.Sprintf("/data/pod%d.txt", index+1)
				content := fmt.Sprintf("Written by pod %d", index+1)
				_, execErr := f.K8s.ExecInPod(ctx, name, []string{
					"sh", "-c", fmt.Sprintf("echo '%s' > %s && sync", content, fileName),
				})
				Expect(execErr).NotTo(HaveOccurred())
			}(i, podName)
		}
		writeWg.Wait()

		By("Verifying all pods can read all files")
		for _, readerPod := range podNames {
			for i := range numPods {
				fileName := fmt.Sprintf("/data/pod%d.txt", i+1)
				expectedContent := fmt.Sprintf("Written by pod %d", i+1)

				output, readErr := f.K8s.ExecInPod(ctx, readerPod, []string{"cat", fileName})
				Expect(readErr).NotTo(HaveOccurred())
				Expect(output).To(Equal(expectedContent),
					fmt.Sprintf("Pod %s should be able to read %s", readerPod, fileName))
			}
		}
	})
})
