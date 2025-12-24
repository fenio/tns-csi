// Package nvmeof contains E2E tests for NVMe-oF volumes.
package nvmeof

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Volume Expansion", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		pvcName          = "expansion-test-nvmeof"
		podName          = "expansion-test-pod-nvmeof"
		storageClassName = "tns-csi-nvmeof"
		initialSize      = "1Gi"
		expandedSize     = "3Gi"
		// NVMe-oF uses WaitForFirstConsumer, needs longer timeouts
		podTimeout       = 360 * time.Second
		expansionTimeout = 180 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should expand ZVOL volume online and resize filesystem while preserving data", func() {
		By("Creating initial PVC (1Gi)")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: storageClassName,
			Size:             initialSize,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Creating pod to bind volume (WaitForFirstConsumer)")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for pod to be ready (triggers volume binding)")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying initial PVC capacity")
		capacity, err := f.K8s.GetPVCCapacity(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(capacity).To(Equal(initialSize))

		By("Writing test data before expansion")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "echo 'Initial data before expansion' > /data/test.txt && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Writing large file to volume (100MB)")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/zero of=/data/largefile bs=1M count=100 && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Getting initial filesystem size")
		initialFsOutput, err := f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "df /data | tail -1 | awk '{print $2}'",
		})
		Expect(err).NotTo(HaveOccurred())
		initialFsBytes, err := strconv.ParseInt(strings.TrimSpace(initialFsOutput), 10, 64)
		Expect(err).NotTo(HaveOccurred())

		By("Getting initial block device size")
		initialDevOutput, err := f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "blockdev --getsize64 $(df /data | tail -1 | awk '{print $1}')",
		})
		Expect(err).NotTo(HaveOccurred())
		initialDevBytes, err := strconv.ParseInt(strings.TrimSpace(initialDevOutput), 10, 64)
		Expect(err).NotTo(HaveOccurred())
		initialDevMB := initialDevBytes / 1024 / 1024

		By("Expanding volume to 3Gi")
		err = f.K8s.ExpandPVC(ctx, pvcName, expandedSize)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC expansion to complete")
		Eventually(func() string {
			capacity, _ := f.K8s.GetPVCCapacity(ctx, pvcName)
			return capacity
		}, expansionTimeout, 5*time.Second).Should(Equal(expandedSize))

		By("Waiting for block device to reflect new size")
		time.Sleep(15 * time.Second) // Give system time to recognize new size

		expandedDevOutput, err := f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "blockdev --getsize64 $(df /data | tail -1 | awk '{print $1}')",
		})
		Expect(err).NotTo(HaveOccurred())
		expandedDevBytes, err := strconv.ParseInt(strings.TrimSpace(expandedDevOutput), 10, 64)
		Expect(err).NotTo(HaveOccurred())
		expandedDevMB := expandedDevBytes / 1024 / 1024

		// Block device should be close to 3GB (> 2500MB)
		Expect(expandedDevMB).To(BeNumerically(">", 2500),
			fmt.Sprintf("Block device should expand to ~3GB, got %dMB", expandedDevMB))

		By(fmt.Sprintf("Block device expanded: %dMB -> %dMB", initialDevMB, expandedDevMB))

		By("Verifying filesystem expansion")
		expandedFsOutput, err := f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "df /data | tail -1 | awk '{print $2}'",
		})
		Expect(err).NotTo(HaveOccurred())
		expandedFsBytes, err := strconv.ParseInt(strings.TrimSpace(expandedFsOutput), 10, 64)
		Expect(err).NotTo(HaveOccurred())

		// Filesystem should be larger than initial
		Expect(expandedFsBytes).To(BeNumerically(">", initialFsBytes),
			fmt.Sprintf("Filesystem should expand (initial: %d, expanded: %d)", initialFsBytes, expandedFsBytes))

		By("Verifying data integrity after expansion")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("Initial data before expansion"))

		By("Verifying large file still exists")
		exists, err := f.K8s.FileExistsInPod(ctx, podName, "/data/largefile")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Large file should still exist after expansion")

		By("Writing additional data to expanded space")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "echo 'Data written after expansion' > /data/test2.txt && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Writing larger file to verify expanded capacity (500MB)")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/zero of=/data/largefile2 bs=1M count=500 && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying new data can be read")
		output, err = f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test2.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("Data written after expansion"))
	})
})
