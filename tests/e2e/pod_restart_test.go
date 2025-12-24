// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("Pod Restart", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		// Setup with nvmeof which enables both NFS and NVMe-oF storage classes
		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should preserve data across graceful and forced pod restarts", func() {
		ctx := context.Background()

		By("Creating an NFS PVC")
		pvcNFS, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "restart-test-nfs",
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NFS PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcNFS.Name)
		})

		By("Creating an NVMe-oF PVC")
		pvcNVMe, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "restart-test-nvmeof",
			StorageClassName: "tns-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NVMe-oF PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcNVMe.Name)
		})

		podName := "restart-test-pod"

		By("Creating pod with both volumes")
		pod, err := createRestartTestPod(ctx, f, podName, pvcNFS.Name, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")
		// Note: cleanup for pod is handled manually in this test due to restarts

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying both PVCs are bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcNFS.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred(), "NFS PVC did not become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcNVMe.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred(), "NVMe-oF PVC did not become Bound")

		By("Recording initial node")
		pod, err = f.K8s.GetPod(ctx, podName)
		Expect(err).NotTo(HaveOccurred())
		initialNode := pod.Spec.NodeName
		GinkgoWriter.Printf("Initial node: %s\n", initialNode)

		By("Writing initial data to NFS volume")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'NFS data v1' > /nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NFS volume")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "date > /nfs/timestamp.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write timestamp to NFS volume")

		By("Writing initial data to NVMe-oF volume")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'NVMe-oF data v1' > /nvmeof/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NVMe-oF volume")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "date > /nvmeof/timestamp.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write timestamp to NVMe-oF volume")

		By("Reading initial data for comparison")
		nfsDataV1, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(nfsDataV1).To(ContainSubstring("NFS data v1"))

		nvmeDataV1, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nvmeof/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(nvmeDataV1).To(ContainSubstring("NVMe-oF data v1"))

		// ========== GRACEFUL RESTART ==========

		By("Performing graceful pod deletion")
		err = f.K8s.DeletePod(ctx, podName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod gracefully")

		By("Waiting for pod to be fully deleted")
		err = f.K8s.WaitForPodDeleted(ctx, podName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod was not deleted in time")

		By("Recreating pod after graceful restart")
		pod, err = createRestartTestPod(ctx, f, podName, pvcNFS.Name, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to recreate pod")

		By("Waiting for recreated pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Recreated pod did not become ready")

		By("Recording node after graceful restart")
		pod, err = f.K8s.GetPod(ctx, podName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Node after graceful restart: %s\n", pod.Spec.NodeName)

		By("Verifying NFS data integrity after graceful restart")
		nfsDataV2, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read NFS data after restart")
		Expect(nfsDataV2).To(Equal(nfsDataV1), "NFS data mismatch after graceful restart")

		By("Verifying NVMe-oF data integrity after graceful restart")
		nvmeDataV2, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nvmeof/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read NVMe-oF data after restart")
		Expect(nvmeDataV2).To(Equal(nvmeDataV1), "NVMe-oF data mismatch after graceful restart")

		By("Appending new data after graceful restart")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'NFS data v2 - after restart' >> /nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'NVMe-oF data v2 - after restart' >> /nvmeof/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred())

		// ========== FORCED TERMINATION ==========

		By("Performing forced pod termination (simulating crash)")
		err = f.K8s.ForceDeletePod(ctx, podName)
		Expect(err).NotTo(HaveOccurred(), "Failed to force delete pod")

		By("Waiting for forced deletion to complete")
		// Give the system time to process forced deletion
		time.Sleep(10 * time.Second)

		By("Recreating pod after forced termination")
		pod, err = createRestartTestPod(ctx, f, podName, pvcNFS.Name, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to recreate pod after forced termination")
		// Register cleanup now that we're done with restarts
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(context.Background(), podName)
		})

		By("Waiting for pod to be ready after forced restart")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready after forced termination")

		By("Verifying all data persisted after forced restart")
		nfsFinal, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read NFS data after forced restart")
		Expect(nfsFinal).To(ContainSubstring("NFS data v1"), "Missing initial NFS data")
		Expect(nfsFinal).To(ContainSubstring("after restart"), "Missing appended NFS data")

		nvmeFinal, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nvmeof/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read NVMe-oF data after forced restart")
		Expect(nvmeFinal).To(ContainSubstring("NVMe-oF data v1"), "Missing initial NVMe-oF data")
		Expect(nvmeFinal).To(ContainSubstring("after restart"), "Missing appended NVMe-oF data")

		By("Writing final verification data")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'Final write after forced restart' > /nfs/final.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write final NFS data")
		_, err = f.K8s.ExecInPod(ctx, podName, []string{"sh", "-c", "echo 'Final write after forced restart' > /nvmeof/final.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write final NVMe-oF data")

		By("Verifying final data written successfully")
		nfsFinalCheck, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nfs/final.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(nfsFinalCheck).To(ContainSubstring("Final write after forced restart"))

		nvmeFinalCheck, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/nvmeof/final.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(nvmeFinalCheck).To(ContainSubstring("Final write after forced restart"))
	})
})

// createRestartTestPod creates a pod with both NFS and NVMe-oF volumes for restart testing.
func createRestartTestPod(ctx context.Context, f *framework.Framework, name, nfsPVCName, nvmeofPVCName string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace(),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "test-container",
					Image:   "public.ecr.aws/docker/library/busybox:latest",
					Command: []string{"sleep", "3600"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "nfs-volume",
							MountPath: "/nfs",
						},
						{
							Name:      "nvmeof-volume",
							MountPath: "/nvmeof",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "nfs-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: nfsPVCName,
						},
					},
				},
				{
					Name: "nvmeof-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: nvmeofPVCName,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	return f.K8s.Clientset().CoreV1().Pods(f.Namespace()).Create(ctx, pod, metav1.CreateOptions{})
}
