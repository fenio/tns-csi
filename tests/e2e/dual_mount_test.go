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

var _ = Describe("Dual Mount", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		// Setup with "both" to enable both NFS and NVMe-oF storage classes
		err = f.Setup("both")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should mount both NFS and NVMe-oF volumes in a single pod", func() {
		ctx := context.Background()

		By("Creating an NFS PVC")
		pvcNFS, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "dual-mount-nfs",
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NFS PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcNFS.Name)
		})

		By("Waiting for NFS PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcNFS.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "NFS PVC did not become Bound")

		By("Creating an NVMe-oF PVC")
		pvcNVMe, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "dual-mount-nvmeof",
			StorageClassName: "tns-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NVMe-oF PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcNVMe.Name)
		})

		By("Creating a pod with both volumes mounted")
		pod, err := createDualMountPod(ctx, f, "dual-mount-pod", pvcNFS.Name, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to create dual-mount pod")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(context.Background(), pod.Name)
		})

		By("Waiting for pod to be ready")
		// NVMe-oF has longer timeout due to WaitForFirstConsumer binding
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying both PVCs are now bound")
		pvcNFS, err = f.K8s.GetPVC(ctx, pvcNFS.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcNFS.Status.Phase).To(Equal(corev1.ClaimBound), "NFS PVC should be bound")

		pvcNVMe, err = f.K8s.GetPVC(ctx, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcNVMe.Status.Phase).To(Equal(corev1.ClaimBound), "NVMe-oF PVC should be bound")

		By("Writing test data to NFS volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'NFS test data' > /data-nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NFS volume")

		By("Writing test data to NVMe-oF volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'NVMe-oF test data' > /data-nvmeof/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NVMe-oF volume")

		By("Reading and verifying NFS data")
		nfsData, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data-nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from NFS volume")
		Expect(nfsData).To(ContainSubstring("NFS test data"), "NFS data mismatch")

		By("Reading and verifying NVMe-oF data")
		nvmeData, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data-nvmeof/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from NVMe-oF volume")
		Expect(nvmeData).To(ContainSubstring("NVMe-oF test data"), "NVMe-oF data mismatch")

		By("Verifying volume isolation - NFS file should not exist on NVMe-oF")
		exists, err := f.K8s.FileExistsInPod(ctx, pod.Name, "/data-nvmeof/test.txt.nfs")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Unexpected cross-volume file access")

		By("Verifying NFS filesystem type")
		mountOutput, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data-nfs"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check NFS mount")
		Expect(mountOutput).To(ContainSubstring("nfs"), "Expected NFS filesystem type")

		By("Verifying NVMe-oF filesystem type")
		mountOutput, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data-nvmeof"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check NVMe-oF mount")
		// NVMe-oF volumes are formatted with ext4 by default
		Expect(mountOutput).To(ContainSubstring("ext4"), "Expected ext4 filesystem on NVMe-oF volume")
	})
})

// createDualMountPod creates a pod with both NFS and NVMe-oF volumes mounted.
func createDualMountPod(ctx context.Context, f *framework.Framework, name, nfsPVCName, nvmeofPVCName string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace(),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "test",
					Image:   "public.ecr.aws/docker/library/busybox:latest",
					Command: []string{"sleep", "3600"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "nfs-volume",
							MountPath: "/data-nfs",
						},
						{
							Name:      "nvmeof-volume",
							MountPath: "/data-nvmeof",
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
