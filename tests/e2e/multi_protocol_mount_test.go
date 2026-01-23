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

var _ = Describe("Multi-Protocol Mount", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		// Setup with "all" to enable NFS, NVMe-oF, and iSCSI storage classes
		err = f.Setup("all")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should mount NFS, NVMe-oF, and iSCSI volumes in a single POD", func() {
		ctx := context.Background()

		By("Creating an NFS PVC")
		pvcNFS, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "multi-mount-nfs",
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
			Name:             "multi-mount-nvmeof",
			StorageClassName: "tns-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create NVMe-oF PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcNVMe.Name)
		})

		By("Creating an iSCSI PVC")
		pvcISCSI, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             "multi-mount-iscsi",
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create iSCSI PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvcISCSI.Name)
		})

		By("Creating a POD with all three volumes mounted")
		pod, err := createMultiMountPod(ctx, f, "multi-mount-pod", pvcNFS.Name, pvcNVMe.Name, pvcISCSI.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to create multi-mount POD")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePod(context.Background(), pod.Name)
		})

		By("Waiting for POD to be ready")
		// Block protocols have longer timeout due to WaitForFirstConsumer binding
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying all PVCs are now bound")
		pvcNFS, err = f.K8s.GetPVC(ctx, pvcNFS.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcNFS.Status.Phase).To(Equal(corev1.ClaimBound), "NFS PVC should be bound")

		pvcNVMe, err = f.K8s.GetPVC(ctx, pvcNVMe.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcNVMe.Status.Phase).To(Equal(corev1.ClaimBound), "NVMe-oF PVC should be bound")

		pvcISCSI, err = f.K8s.GetPVC(ctx, pvcISCSI.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcISCSI.Status.Phase).To(Equal(corev1.ClaimBound), "iSCSI PVC should be bound")

		By("Writing test data to NFS volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'NFS test data' > /data-nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NFS volume")

		By("Writing test data to NVMe-oF volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'NVMe-oF test data' > /data-nvmeof/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to NVMe-oF volume")

		By("Writing test data to iSCSI volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'iSCSI test data' > /data-iscsi/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to iSCSI volume")

		By("Reading and verifying NFS data")
		nfsData, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data-nfs/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from NFS volume")
		Expect(nfsData).To(ContainSubstring("NFS test data"), "NFS data mismatch")

		By("Reading and verifying NVMe-oF data")
		nvmeData, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data-nvmeof/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from NVMe-oF volume")
		Expect(nvmeData).To(ContainSubstring("NVMe-oF test data"), "NVMe-oF data mismatch")

		By("Reading and verifying iSCSI data")
		iscsiData, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data-iscsi/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from iSCSI volume")
		Expect(iscsiData).To(ContainSubstring("iSCSI test data"), "iSCSI data mismatch")

		By("Verifying volume isolation - NFS file should not exist on block volumes")
		exists, err := f.K8s.FileExistsInPod(ctx, pod.Name, "/data-nvmeof/test.txt.nfs")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Unexpected cross-volume file access on NVMe-oF")

		exists, err = f.K8s.FileExistsInPod(ctx, pod.Name, "/data-iscsi/test.txt.nfs")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Unexpected cross-volume file access on iSCSI")

		By("Verifying NFS filesystem type")
		mountOutput, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data-nfs"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check NFS mount")
		Expect(mountOutput).To(ContainSubstring("nfs"), "Expected NFS filesystem type")

		By("Verifying NVMe-oF filesystem type")
		mountOutput, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data-nvmeof"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check NVMe-oF mount")
		// NVMe-oF volumes are formatted with ext4 by default
		Expect(mountOutput).To(ContainSubstring("ext4"), "Expected ext4 filesystem on NVMe-oF volume")

		By("Verifying iSCSI filesystem type")
		mountOutput, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mount | grep /data-iscsi"})
		Expect(err).NotTo(HaveOccurred(), "Failed to check iSCSI mount")
		// iSCSI volumes are formatted with ext4 by default
		Expect(mountOutput).To(ContainSubstring("ext4"), "Expected ext4 filesystem on iSCSI volume")

		if f.Verbose() {
			GinkgoWriter.Printf("Multi-protocol mount test completed successfully\n")
			GinkgoWriter.Printf("  - NFS volume mounted and verified\n")
			GinkgoWriter.Printf("  - NVMe-oF volume mounted and verified\n")
			GinkgoWriter.Printf("  - iSCSI volume mounted and verified\n")
		}
	})
})

// createMultiMountPod creates a pod with NFS, NVMe-oF, and iSCSI volumes mounted.
func createMultiMountPod(ctx context.Context, f *framework.Framework, name, nfsPVCName, nvmeofPVCName, iscsiPVCName string) (*corev1.Pod, error) {
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
						{
							Name:      "iscsi-volume",
							MountPath: "/data-iscsi",
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
				{
					Name: "iscsi-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: iscsiPVCName,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	return f.K8s.Clientset().CoreV1().Pods(f.Namespace()).Create(ctx, pod, metav1.CreateOptions{})
}
