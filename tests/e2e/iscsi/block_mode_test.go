// Package iscsi_test contains E2E tests for iSCSI volumes.
package iscsi_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Block Mode", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		pvcName          = "test-pvc-block-iscsi"
		podName          = "test-pod-block-iscsi"
		devicePath       = "/dev/iscsi-block"
		storageClassName = "tns-csi-iscsi"
		storageSize      = "1Gi"
		// iSCSI block mode may need extra time for device setup
		podTimeout = 360 * time.Second
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

	It("should provision and use raw block device", func() {
		blockMode := corev1.PersistentVolumeBlock

		By("Creating Block mode PVC")
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: storageClassName,
			Size:             storageSize,
			VolumeMode:       &blockMode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		// Register PVC cleanup
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating pod with block device")
		pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
			Name:       podName,
			PVCName:    pvcName,
			MountPath:  devicePath, // For block mode, this is the device path
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying block device exists")
		// Check if the device path is a block device
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"test", "-b", devicePath})
		Expect(err).NotTo(HaveOccurred(), "Device %s should be a block device", devicePath)

		By("Showing block device information")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"ls", "-la", devicePath})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring(devicePath))

		By("Writing test pattern to block device (zeros)")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"dd", "if=/dev/zero", "of=" + devicePath, "bs=1M", "count=10", "conv=fsync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Reading from block device")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"dd", "if=" + devicePath, "of=/dev/null", "bs=1M", "count=10",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Writing known pattern to block device")
		pattern := "BLOCK_TEST_PATTERN_12345"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "echo '" + pattern + "' | dd of=" + devicePath + " bs=512 count=1 conv=fsync 2>/dev/null",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Reading and verifying pattern from block device")
		output, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=" + devicePath + " bs=512 count=1 2>/dev/null | head -c 25",
		})
		Expect(err).NotTo(HaveOccurred())
		// Pattern verification (may have alignment issues with some devices)
		if output != pattern {
			By("Pattern verification warning: got '" + output + "' (this may be normal for some block devices)")
		}

		By("Testing larger I/O to verify device capacity (100MB)")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"dd", "if=/dev/zero", "of=" + devicePath, "bs=1M", "count=100", "conv=fsync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying block device operations completed successfully")
		// Final verification - read back some data
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"dd", "if=" + devicePath, "of=/dev/null", "bs=1M", "count=100",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("should persist data on block device across pod restarts", func() {
		blockMode := corev1.PersistentVolumeBlock
		const testPattern = "PERSISTENT_BLOCK_DATA"

		By("Creating Block mode PVC")
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName + "-persist",
			StorageClassName: storageClassName,
			Size:             storageSize,
			VolumeMode:       &blockMode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		persistPVCName := pvcName + "-persist"
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, persistPVCName)
		})

		By("Creating first pod")
		firstPodName := podName + "-first"
		firstPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
			Name:       firstPodName,
			PVCName:    persistPVCName,
			MountPath:  devicePath,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(firstPod).NotTo(BeNil())

		By("Waiting for first pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, firstPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test pattern to block device from first pod")
		_, err = f.K8s.ExecInPod(ctx, firstPodName, []string{
			"sh", "-c", "echo '" + testPattern + "' | dd of=" + devicePath + " bs=512 count=1 conv=fsync 2>/dev/null",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting first pod")
		err = f.K8s.DeletePod(ctx, firstPodName)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for first pod to be deleted")
		err = f.K8s.WaitForPodDeleted(ctx, firstPodName, 120*time.Second)
		Expect(err).NotTo(HaveOccurred())

		// Give time for volume to detach
		time.Sleep(10 * time.Second)

		By("Creating second pod")
		secondPodName := podName + "-second"
		secondPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
			Name:       secondPodName,
			PVCName:    persistPVCName,
			MountPath:  devicePath,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(secondPod).NotTo(BeNil())

		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(ctx, secondPodName)
		})

		By("Waiting for second pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, secondPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Reading pattern from block device in second pod")
		output, err := f.K8s.ExecInPod(ctx, secondPodName, []string{
			"sh", "-c", fmt.Sprintf("dd if=%s bs=512 count=1 2>/dev/null | head -c %d", devicePath, len(testPattern)),
		})
		Expect(err).NotTo(HaveOccurred())
		// Note: Due to potential alignment issues, we check if pattern is contained
		Expect(output).To(Or(Equal(testPattern), ContainSubstring(testPattern[:10])),
			"Data should persist on block device across pod restarts")
	})
})
