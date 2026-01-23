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

var _ = Describe("NVMe-oF Crash Simulation", func() {
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

	It("should persist data after POD crash (force delete)", func() {
		ctx := context.Background()
		timestamp := time.Now().Unix()
		testData := fmt.Sprintf("Crash Test Data - %d", timestamp)

		By("Creating PVC for crash simulation test")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             fmt.Sprintf("crash-pvc-nvmeof-%d", timestamp),
			StorageClassName: "tns-csi-nvmeof",
			Size:             "2Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Creating POD and writing test data")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      fmt.Sprintf("crash-pod-nvmeof-%d", timestamp),
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Writing test data to volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt", testData)})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Writing large file for integrity verification (5MB)")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "dd if=/dev/urandom of=/data/large-file.bin bs=1M count=5"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write large file")

		By("Calculating checksum of large file")
		checksumOutput, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "md5sum /data/large-file.bin | awk '{print $1}'"})
		Expect(err).NotTo(HaveOccurred(), "Failed to calculate checksum")
		originalChecksum := strings.TrimSpace(checksumOutput)
		GinkgoWriter.Printf("Original checksum: %s\n", originalChecksum)

		By("Syncing filesystem to ensure data is written")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")

		By("Simulating POD crash with force delete")
		err = f.K8s.ForceDeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to force delete POD")

		time.Sleep(10 * time.Second) // Wait for POD to be fully removed

		By("Creating new POD to verify data survived crash")
		newPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      fmt.Sprintf("crash-pod-nvmeof-recovery-%d", timestamp),
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create recovery POD")

		err = f.K8s.WaitForPodReady(ctx, newPod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Recovery POD did not become ready")

		By("Verifying test data persisted after crash")
		output, err := f.K8s.ExecInPod(ctx, newPod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data after crash")
		Expect(output).To(Equal(testData), "Data mismatch after crash")

		By("Verifying large file integrity after crash")
		checksumOutput, err = f.K8s.ExecInPod(ctx, newPod.Name, []string{"sh", "-c", "md5sum /data/large-file.bin | awk '{print $1}'"})
		Expect(err).NotTo(HaveOccurred(), "Failed to calculate checksum after crash")
		newChecksum := strings.TrimSpace(checksumOutput)
		Expect(newChecksum).To(Equal(originalChecksum), "Large file corrupted after crash")

		By("Listing final file structure")
		output, err = f.K8s.ExecInPod(ctx, newPod.Name, []string{"ls", "-la", "/data/"})
		Expect(err).NotTo(HaveOccurred(), "Failed to list files")
		GinkgoWriter.Printf("Files after crash recovery:\n%s\n", output)
	})
})
