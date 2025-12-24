// Package nvmeof contains E2E tests for NVMe-oF protocol support.
package nvmeof

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Data Persistence", func() {
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

	It("should persist data across pod restarts and crashes", func() {
		ctx := context.Background()
		timestamp := time.Now().Unix()
		testData := fmt.Sprintf("Persistence Test Data - %d", timestamp)

		By("Creating PVC for persistence test")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             fmt.Sprintf("persistence-pvc-nvmeof-%d", timestamp),
			StorageClassName: "tns-csi-nvmeof",
			Size:             "2Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Getting bound PV name for verification")
		boundPVC, err := f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC")
		pvName := boundPVC.Spec.VolumeName
		GinkgoWriter.Printf("Created PV: %s\n", pvName)

		By("Creating initial pod and writing test data")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      fmt.Sprintf("persistence-pod-nvmeof-%d", timestamp),
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Writing test data to volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt", testData)})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Verifying test data immediately after write")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data immediately after write")
		Expect(output).To(Equal(testData), "Data mismatch immediately after write")

		By("Writing large file for integrity verification (50MB)")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "dd if=/dev/urandom of=/data/large-file.bin bs=1M count=50"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write large file")

		By("Calculating checksum of large file")
		checksumOutput, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "md5sum /data/large-file.bin | awk '{print $1}'"})
		Expect(err).NotTo(HaveOccurred(), "Failed to calculate checksum")
		originalChecksum := strings.TrimSpace(checksumOutput)
		GinkgoWriter.Printf("Original checksum: %s\n", originalChecksum)

		By("Creating nested directory structure")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mkdir -p /data/subdir1/subdir2 && echo 'nested data' > /data/subdir1/subdir2/nested.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to create nested directories")

		By("Syncing filesystem to ensure data is written")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")

		// Test 1: Graceful pod restart
		By("Test 1: Graceful pod restart - deleting pod gracefully")
		err = f.K8s.DeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")

		err = f.K8s.WaitForPodDeleted(ctx, pod.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod was not deleted")

		time.Sleep(5 * time.Second) // Give CSI driver time to process unpublish

		By("Recreating pod with same PVC")
		pod, err = f.CreatePod(ctx, framework.PodOptions{
			Name:      fmt.Sprintf("persistence-pod-nvmeof-%d", timestamp),
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to recreate pod")

		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Recreated pod did not become ready")

		By("Listing files on volume after restart")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"ls", "-la", "/data/"})
		Expect(err).NotTo(HaveOccurred(), "Failed to list files after restart")
		GinkgoWriter.Printf("Files after restart:\n%s\n", output)

		By("Verifying test data persisted after graceful restart")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data after restart")
		Expect(output).To(Equal(testData), "Data mismatch after graceful restart")

		By("Verifying large file integrity after graceful restart")
		checksumOutput, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "md5sum /data/large-file.bin | awk '{print $1}'"})
		Expect(err).NotTo(HaveOccurred(), "Failed to calculate checksum after restart")
		newChecksum := strings.TrimSpace(checksumOutput)
		Expect(newChecksum).To(Equal(originalChecksum), "Large file corrupted after graceful restart")

		By("Verifying nested directory structure after graceful restart")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/subdir1/subdir2/nested.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read nested file after restart")
		Expect(output).To(Equal("nested data"), "Nested data mismatch after graceful restart")

		// Test 2: Force delete (simulated crash)
		By("Test 2: Force delete (simulated crash)")
		err = f.K8s.ForceDeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to force delete pod")

		time.Sleep(10 * time.Second) // Wait for pod to be fully removed

		By("Creating new pod with different name after crash")
		pod2Name := fmt.Sprintf("persistence-pod-nvmeof-2-%d", timestamp)
		pod2, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      pod2Name,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create new pod after crash")

		err = f.K8s.WaitForPodReady(ctx, pod2.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "New pod did not become ready after crash")

		By("Verifying PVC is still bound to the same PV")
		boundPVC, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC after crash")
		Expect(boundPVC.Spec.VolumeName).To(Equal(pvName), "PVC was rebound to a different PV!")

		By("Listing files on volume after force delete")
		output, err = f.K8s.ExecInPod(ctx, pod2.Name, []string{"ls", "-la", "/data/"})
		Expect(err).NotTo(HaveOccurred(), "Failed to list files after force delete")
		GinkgoWriter.Printf("Files after force delete:\n%s\n", output)

		By("Verifying test data persisted after force delete")
		output, err = f.K8s.ExecInPod(ctx, pod2.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data after crash")
		Expect(output).To(Equal(testData), "Data mismatch after force delete")

		By("Verifying large file integrity after force delete")
		checksumOutput, err = f.K8s.ExecInPod(ctx, pod2.Name, []string{"sh", "-c", "md5sum /data/large-file.bin | awk '{print $1}'"})
		Expect(err).NotTo(HaveOccurred(), "Failed to calculate checksum after crash")
		newChecksum = strings.TrimSpace(checksumOutput)
		Expect(newChecksum).To(Equal(originalChecksum), "Large file corrupted after force delete")

		By("Verifying nested directory structure after force delete")
		output, err = f.K8s.ExecInPod(ctx, pod2.Name, []string{"cat", "/data/subdir1/subdir2/nested.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read nested file after crash")
		Expect(output).To(Equal("nested data"), "Nested data mismatch after force delete")

		// Test 3: Write new data and verify persistence across another restart
		By("Test 3: Writing new data from second pod")
		_, err = f.K8s.ExecInPod(ctx, pod2.Name, []string{"sh", "-c", "echo 'Data from second pod' > /data/second-pod.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write new data")

		err = f.K8s.DeletePod(ctx, pod2.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete second pod")

		err = f.K8s.WaitForPodDeleted(ctx, pod2.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Second pod was not deleted")

		By("Creating third pod to verify new data persisted")
		pod3, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      fmt.Sprintf("persistence-pod-nvmeof-3-%d", timestamp),
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create third pod")

		err = f.K8s.WaitForPodReady(ctx, pod3.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Third pod did not become ready")

		By("Verifying data from second pod persisted")
		output, err = f.K8s.ExecInPod(ctx, pod3.Name, []string{"cat", "/data/second-pod.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read second pod data")
		Expect(output).To(Equal("Data from second pod"), "Data from second pod was lost")

		By("Listing final file structure")
		output, err = f.K8s.ExecInPod(ctx, pod3.Name, []string{"sh", "-c", "find /data -type f -exec ls -lh {} \\;"})
		Expect(err).NotTo(HaveOccurred(), "Failed to list files")
		GinkgoWriter.Printf("Final file structure:\n%s\n", output)
	})
})
