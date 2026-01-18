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

var _ = Describe("iSCSI ZFS Properties", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts (longer for iSCSI block devices)
	const (
		pvcTimeout = 360 * time.Second
		podTimeout = 360 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
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

	It("should create ZVOL with custom ZFS properties", func() {
		By("Creating StorageClass with ZFS properties for iSCSI")
		zfsStorageClass := "tns-csi-iscsi-zfsprops"
		err = f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, zfsStorageClass, "tns.csi.io", map[string]string{
			"protocol":         "iscsi",
			"server":           f.Config.TrueNASHost,
			"pool":             f.Config.TrueNASPool,
			"port":             "3260",
			"fsType":           "ext4",
			"zfs.compression":  "lz4",
			"zfs.volblocksize": "16K",
		}, "WaitForFirstConsumer")
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, zfsStorageClass)
		})

		By("Creating a PVC with ZFS properties StorageClass")
		pvcName := "test-pvc-iscsi-zfsprops"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: zfsStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating a pod to trigger PVC binding and test volume I/O")
		podName := "test-pod-iscsi-zfsprops"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(ctx, podName)
		})

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test data to verify volume is usable")
		testData := "iSCSI ZFS Properties Test Data"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Reading back test data")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData))

		By("Writing larger data to test block device")
		// Write 50MB to verify ZVOL is working correctly
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/zero of=/data/testfile.bin bs=1M count=50 && sync",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying file was created")
		output, err = f.K8s.ExecInPod(ctx, podName, []string{"ls", "-la", "/data/testfile.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("testfile.bin"))

		By("Checking controller logs for ZVOL creation")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Controller logs (ZVOL properties check):\n%s\n", logs)
	})
})
