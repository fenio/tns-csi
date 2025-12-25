// Package nfs contains E2E tests for NFS protocol support.
package nfs

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NFS Mount and Expansion", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should provision an NFS volume, mount it, perform I/O, and expand it", func() {
		ctx := context.Background()

		By("Creating a PVC")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "test-pvc-nfs",
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Verifying PVC is Bound")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound))

		By("Creating a test pod")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "test-pod-nfs",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")
		Expect(pod).NotTo(BeNil())

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Writing data to the volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'hello world' > /data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write data")

		By("Reading data from the volume")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read data")
		Expect(output).To(ContainSubstring("hello world"))

		By("Verifying filesystem type is nfs")
		mountOutput, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check mount")
		Expect(mountOutput).To(ContainSubstring("nfs"))

		By("Expanding the PVC to 3Gi")
		err = f.K8s.ExpandPVC(ctx, pvc.Name, "3Gi")
		Expect(err).NotTo(HaveOccurred(), "Failed to expand PVC")

		By("Waiting for expansion to complete")
		Eventually(func() string {
			capacity, _ := f.K8s.GetPVCCapacity(ctx, pvc.Name)
			return capacity
		}, 2*time.Minute, 5*time.Second).Should(Equal("3Gi"), "PVC did not expand to 3Gi")

		By("Writing more data to verify expanded volume works")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "dd if=/dev/zero of=/data/bigfile bs=1M count=100"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to expanded volume")

		By("Verifying the file was created")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"ls", "-la", "/data/bigfile"})
		Expect(err).NotTo(HaveOccurred(), "Failed to verify file")
		Expect(output).To(ContainSubstring("bigfile"))
	})
})
