package nvmeof_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Access Modes", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts (longer for NVMe-oF)
	const (
		pvcTimeout = 360 * time.Second
		podTimeout = 360 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
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

	Context("ReadWriteOnce (RWO)", func() {
		It("should allow single pod to mount and use the block volume", func() {
			By("Creating a RWO PVC with NVMe-oF")
			pvcName := "nvmeof-rwo"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nvmeof",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			By("Creating a pod to mount the volume (triggers binding)")
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

			By("Waiting for pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing and reading data to verify volume works")
			testData := "NVMe-oF RWO Test Data"
			_, err = f.K8s.ExecInPod(ctx, podName, []string{
				"sh", "-c", "echo '" + testData + "' > /data/test.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))
		})
	})

	Context("ReadWriteOncePod (RWOP)", func() {
		It("should allow single pod exclusive access to the volume", func() {
			By("Creating a RWOP PVC with NVMe-oF")
			pvcName := "nvmeof-rwop"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nvmeof",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			By("Creating a pod to mount the RWOP volume")
			pod1Name := "rwop-test-pod-1"
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

			By("Waiting for first pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod1Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing data to the RWOP volume")
			testData := "Exclusive RWOP Data"
			_, err = f.K8s.ExecInPod(ctx, pod1Name, []string{
				"sh", "-c", "echo '" + testData + "' > /data/exclusive.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Attempting to create a second pod with the same RWOP volume")
			pod2Name := "rwop-violation-pod"
			_, err = f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      pod2Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod2Name)
			})

			By("Waiting to see if second pod gets blocked")
			// The second pod should remain in Pending state because RWOP enforces single-pod access
			time.Sleep(15 * time.Second)

			By("Checking second pod status")
			pod2, err := f.K8s.GetPod(ctx, pod2Name)
			Expect(err).NotTo(HaveOccurred())

			// RWOP should keep the second pod in Pending state
			Expect(pod2.Status.Phase).To(Equal(corev1.PodPending),
				"Second pod should be blocked from mounting RWOP volume")

			By("Verifying first pod still has exclusive access")
			output, err := f.K8s.ExecInPod(ctx, pod1Name, []string{"cat", "/data/exclusive.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData), "First pod should still have access to data")

			By("Deleting the violating pod")
			err = f.K8s.DeletePod(ctx, pod2Name)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should allow second pod access after first pod is deleted", func() {
			By("Creating a RWOP PVC with NVMe-oF")
			pvcName := "nvmeof-rwop-succession"
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: "tns-csi-nvmeof",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc).NotTo(BeNil())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			By("Creating first pod to mount the RWOP volume")
			pod1Name := "rwop-first-pod"
			_, err = f.CreatePod(ctx, framework.PodOptions{
				Name:      pod1Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod1Name)
			})

			By("Waiting for first pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod1Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing data from first pod")
			testData := "Data from first pod"
			_, err = f.K8s.ExecInPod(ctx, pod1Name, []string{
				"sh", "-c", "echo '" + testData + "' > /data/succession.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Deleting first pod")
			err = f.K8s.DeletePod(ctx, pod1Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod1Name, 120*time.Second)
			Expect(err).NotTo(HaveOccurred())

			By("Creating second pod to mount the same RWOP volume")
			pod2Name := "rwop-second-pod"
			_, err = f.CreatePod(ctx, framework.PodOptions{
				Name:      pod2Name,
				PVCName:   pvcName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, pod2Name)
			})

			By("Waiting for second pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod2Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second pod can read data written by first pod")
			output, err := f.K8s.ExecInPod(ctx, pod2Name, []string{"cat", "/data/succession.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData), "Second pod should see data from first pod")

			By("Writing new data from second pod")
			newData := "Data from second pod"
			_, err = f.K8s.ExecInPod(ctx, pod2Name, []string{
				"sh", "-c", "echo '" + newData + "' > /data/new-data.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second pod write succeeded")
			output, err = f.K8s.ExecInPod(ctx, pod2Name, []string{"cat", "/data/new-data.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(newData))
		})
	})
})
