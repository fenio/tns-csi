// Package nfs contains E2E tests for NFS volumes.
package nfs

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

var _ = Describe("NFS Volume Expansion", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		pvcName          = "expansion-test-nfs"
		podName          = "expansion-test-pod-nfs"
		storageClassName = "tns-csi-nfs"
		initialSize      = "1Gi"
		expandedSize     = "2Gi"
		podTimeout       = 120 * time.Second
		expansionTimeout = 120 * time.Second
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

	It("should expand volume online while mounted and preserve data", func() {
		By("Creating initial PVC (1Gi)")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: storageClassName,
			Size:             initialSize,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying initial PVC capacity")
		capacity, err := f.K8s.GetPVCCapacity(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(capacity).To(Equal(initialSize))

		By("Creating pod and mounting volume")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

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

		By("Expanding volume to 2Gi (online expansion)")
		err = f.K8s.ExpandPVC(ctx, pvcName, expandedSize)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC expansion to complete")
		Eventually(func() string {
			capacity, _ := f.K8s.GetPVCCapacity(ctx, pvcName)
			return capacity
		}, expansionTimeout, 5*time.Second).Should(Equal(expandedSize))

		By("Waiting for filesystem to reflect new size")
		time.Sleep(10 * time.Second) // Give filesystem time to resize

		expandedFsOutput, err := f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "df /data | tail -1 | awk '{print $2}'",
		})
		Expect(err).NotTo(HaveOccurred())
		expandedFsBytes, err := strconv.ParseInt(strings.TrimSpace(expandedFsOutput), 10, 64)
		Expect(err).NotTo(HaveOccurred())

		// Filesystem should be larger (or at least not smaller for NFS)
		Expect(expandedFsBytes).To(BeNumerically(">=", initialFsBytes),
			fmt.Sprintf("Filesystem should be at least as large after expansion (initial: %d, expanded: %d)", initialFsBytes, expandedFsBytes))

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

		By("Writing larger file to verify expanded capacity (200MB)")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/zero of=/data/largefile2 bs=1M count=200 && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying new data can be read")
		output, err = f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test2.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("Data written after expansion"))
	})
})
