package nfs_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NFS Encryption", func() {
	var f *framework.Framework

	// Timeouts
	const (
		pvcTimeout = 120 * time.Second
		podTimeout = 120 * time.Second
	)

	BeforeEach(func() {
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

	It("should create encrypted NFS volume with auto-generated key", func() {
		ctx := context.Background()

		By("Creating StorageClass with encryption enabled (auto-generate key)")
		encryptedStorageClass := "tns-csi-nfs-encrypted"
		err := f.K8s.CreateStorageClassWithParams(ctx, encryptedStorageClass, "tns.csi.io", map[string]string{
			"protocol":              "nfs",
			"server":                f.Config.TrueNASHost,
			"pool":                  f.Config.TrueNASPool,
			"encryption":            "true",
			"encryptionGenerateKey": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, encryptedStorageClass)
		})

		By("Creating a PVC with encrypted StorageClass")
		pvcName := "test-pvc-encrypted-nfs"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: encryptedStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a pod to test volume I/O")
		podName := "test-pod-encrypted-nfs"
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

		By("Writing test data to encrypted volume")
		testData := "Encryption Test Data - Sensitive Information"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/secret.txt", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Reading back test data from encrypted volume")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/secret.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData))

		By("Writing binary data to verify encryption doesn't corrupt data")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/urandom of=/data/random.bin bs=1M count=5 2>/dev/null",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying binary file checksum")
		checksumBefore, err := f.K8s.ExecInPod(ctx, podName, []string{"md5sum", "/data/random.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(checksumBefore).NotTo(BeEmpty())

		By("Re-reading and verifying checksum matches")
		checksumAfter, err := f.K8s.ExecInPod(ctx, podName, []string{"md5sum", "/data/random.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(checksumAfter).To(Equal(checksumBefore))

		By("Checking controller logs for encryption application")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Controller logs (encryption check):\n%s\n", logs)
	})

	It("should expand encrypted NFS volume", func() {
		ctx := context.Background()

		By("Creating StorageClass with encryption enabled")
		encryptedStorageClass := "tns-csi-nfs-encrypted-expand"
		err := f.K8s.CreateStorageClassWithParams(ctx, encryptedStorageClass, "tns.csi.io", map[string]string{
			"protocol":              "nfs",
			"server":                f.Config.TrueNASHost,
			"pool":                  f.Config.TrueNASPool,
			"encryption":            "true",
			"encryptionGenerateKey": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, encryptedStorageClass)
		})

		By("Creating a PVC with encrypted StorageClass")
		pvcName := "test-pvc-encrypted-expand"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: encryptedStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a pod to mount the volume")
		podName := "test-pod-encrypted-expand"
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

		By("Writing initial data")
		testData := "Data before expansion"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Expanding the encrypted PVC to 3Gi")
		err = f.K8s.ExpandPVC(ctx, pvcName, "3Gi")
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for expansion to complete")
		Eventually(func() string {
			capacity, _ := f.K8s.GetPVCCapacity(ctx, pvcName)
			return capacity
		}, 2*time.Minute, 5*time.Second).Should(Equal("3Gi"), "PVC did not expand to 3Gi")

		By("Verifying original data is still readable after expansion")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData))

		By("Writing additional data to expanded space")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", "dd if=/dev/zero of=/data/bigfile bs=1M count=100 2>/dev/null",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the additional file was created")
		output, err = f.K8s.ExecInPod(ctx, podName, []string{"ls", "-la", "/data/bigfile"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("bigfile"))
	})

	It("should create encrypted NFS volume with custom algorithm", func() {
		ctx := context.Background()

		By("Creating StorageClass with encryption and custom algorithm (AES-128-CCM)")
		encryptedStorageClass := "tns-csi-nfs-encrypted-algorithm"
		err := f.K8s.CreateStorageClassWithParams(ctx, encryptedStorageClass, "tns.csi.io", map[string]string{
			"protocol":              "nfs",
			"server":                f.Config.TrueNASHost,
			"pool":                  f.Config.TrueNASPool,
			"encryption":            "true",
			"encryptionAlgorithm":   "AES-128-CCM",
			"encryptionGenerateKey": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, encryptedStorageClass)
		})

		By("Creating a PVC with custom algorithm StorageClass")
		pvcName := "test-pvc-encrypted-algorithm"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: encryptedStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a pod to test volume I/O")
		podName := "test-pod-encrypted-algorithm"
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

		By("Writing test data to verify volume works with custom algorithm")
		testData := "Custom Algorithm Encryption Test"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/algorithm-test.txt", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Reading back test data")
		output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/algorithm-test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData))
	})
})
