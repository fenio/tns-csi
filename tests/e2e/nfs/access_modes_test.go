package nfs_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NFS Access Modes", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts
	const (
		pvcTimeout = 120 * time.Second
		podTimeout = 120 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
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

	Context("ReadWriteMany (RWX)", func() {
		It("should allow multiple PODs to mount the same volume concurrently", func() {
			By("Creating a RWX PVC")
			pvcName := "access-mode-rwx"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nfs",
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

			By("Creating first POD mounting the RWX volume")
			pod1Name := "access-test-pod-1"
			pod1, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      pod1Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pod1).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod1Name)
			})

			By("Waiting for first POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod1Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing to POD")
			_, err = f.K8s.ExecInPod(ctx, pod1Name, []string{
				"sh", "-c", "echo 'Data from POD 1' > /data/pod1.txt",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating second POD mounting the same RWX volume")
			pod2Name := "access-test-pod-2"
			pod2, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      pod2Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pod2).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod2Name)
			})

			By("Waiting for second POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod2Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second POD can read data written by first pod")
			output, err := f.K8s.ExecInPod(ctx, pod2Name, []string{"cat", "/data/pod1.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Data from POD 1"), "POD 2 should read data from POD 1")

			By("Writing to POD")
			_, err = f.K8s.ExecInPod(ctx, pod2Name, []string{
				"sh", "-c", "echo 'Data from POD 2' > /data/pod2.txt",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying first POD can read data written by second pod")
			output, err = f.K8s.ExecInPod(ctx, pod1Name, []string{"cat", "/data/pod2.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Data from POD 2"), "POD 1 should read data from POD 2")
		})

		It("should handle concurrent writes from multiple PODs", func() {
			By("Creating a RWX PVC")
			pvcName := "concurrent-rwx"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nfs",
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

			By("Creating first POD")
			pod1Name := "concurrent-pod-1"
			_, err = f.CreatePod(ctx, framework.PodOptions{
				Name:      pod1Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod1Name)
			})

			By("Creating second POD")
			pod2Name := "concurrent-pod-2"
			_, err = f.CreatePod(ctx, framework.PodOptions{
				Name:      pod2Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod2Name)
			})

			By("Waiting for both PODs to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod1Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, pod2Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Performing concurrent writes from both PODs")
			// Write from POD 1 (5 lines)
			_, err = f.K8s.ExecInPod(ctx, pod1Name, []string{
				"sh", "-c", "for i in 1 2 3 4 5; do echo \"pod1-$i\" >> /data/concurrent.txt; done",
			})
			Expect(err).NotTo(HaveOccurred())

			// Write from POD 2 (5 lines)
			_, err = f.K8s.ExecInPod(ctx, pod2Name, []string{
				"sh", "-c", "for i in 1 2 3 4 5; do echo \"pod2-$i\" >> /data/concurrent.txt; done",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both PODs wrote to the shared file")
			output, err := f.K8s.ExecInPod(ctx, pod1Name, []string{"cat", "/data/concurrent.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("pod1-"), "File should contain data from POD 1")
			Expect(output).To(ContainSubstring("pod2-"), "File should contain data from POD 2")

			By("Counting total lines written")
			countOutput, err := f.K8s.ExecInPod(ctx, pod1Name, []string{
				"sh", "-c", "wc -l < /data/concurrent.txt",
			})
			Expect(err).NotTo(HaveOccurred())
			// Both pods wrote 5 lines each
			Expect(countOutput).To(Equal("10"), "File should have 10 lines total")
		})
	})

	Context("ReadWriteOnce (RWO)", func() {
		It("should allow single POD to mount and use the volume", func() {
			By("Creating a RWO PVC with NFS")
			pvcName := "access-mode-rwo"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a POD to mount the RWO volume")
			podName := "rwo-test-pod"
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

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing and reading data to verify volume works")
			testData := "RWO Test Data"
			_, err = f.K8s.ExecInPod(ctx, podName, []string{
				"sh", "-c", "echo '" + testData + "' > /data/test.txt",
			})
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))
		})
	})
})
