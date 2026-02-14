package scale

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("CSI Operations with Non-CSI Data", Ordered, func() {
	// Use Ordered so the noise integrity check runs last.

	Describe("Volume Lifecycle", func() {
		var f *framework.Framework

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

		It("should provision, mount, perform I/O, and delete an NFS volume", func() {
			ctx := context.Background()

			By("Creating a PVC amid noise data")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "scale-basic-pvc",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a test pod")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-basic-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Writing data to the volume")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'scale test data' > /data/test.txt"})
			Expect(err).NotTo(HaveOccurred())

			By("Reading data from the volume")
			output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("scale test data"))

			// Cleanup is handled by framework teardown (LIFO: pod -> PVC)
		})

		It("should expand a volume", func() {
			ctx := context.Background()

			By("Creating a PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "scale-expand-pvc",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a test pod")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-expand-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Expanding the PVC to 3Gi")
			err = f.K8s.ExpandPVC(ctx, pvc.Name, "3Gi")
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for expansion to complete")
			Eventually(func() string {
				capacity, _ := f.K8s.GetPVCCapacity(ctx, pvc.Name)
				return capacity
			}, 2*time.Minute, 5*time.Second).Should(Equal("3Gi"))

			By("Writing data to verify expanded volume works")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "dd if=/dev/zero of=/data/bigfile bs=1M count=50"})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle concurrent volume creation", func() {
			ctx := context.Background()
			pvcCount := 3

			By(fmt.Sprintf("Creating %d PVCs concurrently", pvcCount))
			for i := 1; i <= pvcCount; i++ {
				pvcName := fmt.Sprintf("scale-concurrent-pvc-%d", i)
				pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
					Name:             pvcName,
					StorageClassName: "tns-csi-nfs",
					Size:             "1Gi",
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create PVC %s", pvcName)
				Expect(pvc).NotTo(BeNil())
			}

			By("Waiting for all PVCs to become Bound")
			for i := 1; i <= pvcCount; i++ {
				pvcName := fmt.Sprintf("scale-concurrent-pvc-%d", i)
				err := f.K8s.WaitForPVCBound(ctx, pvcName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "PVC %s did not become Bound", pvcName)
			}
		})
	})

	Describe("Snapshot Operations", func() {
		var f *framework.Framework

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

		It("should create a snapshot and restore from it", func() {
			ctx := context.Background()
			snapshotClass := "tns-csi-nfs-snapshot-scale"

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "scale-snap-source-pvc",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating source pod and writing data")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-snap-source-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'snapshot data v1' > /data/version.txt && sync"})
			Expect(err).NotTo(HaveOccurred())

			By("Creating a snapshot amid noise data")
			snapshotName := "scale-snap-1"
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
			})

			By("Waiting for snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Modifying source data after snapshot")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'snapshot data v2' > /data/version.txt && sync"})
			Expect(err).NotTo(HaveOccurred())

			By("Restoring PVC from snapshot")
			restorePVC := "scale-snap-restore-pvc"
			err = f.K8s.CreatePVCFromSnapshot(ctx, restorePVC, snapshotName, "tns-csi-nfs", "1Gi",
				[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
			Expect(err).NotTo(HaveOccurred())
			f.RegisterPVCCleanup(restorePVC)

			By("Creating pod for restored PVC")
			restorePod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-snap-restore-pod",
				PVCName:   restorePVC,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, restorePod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying restored data matches snapshot (v1, not v2)")
			output, err := f.K8s.ExecInPod(ctx, restorePod.Name, []string{"cat", "/data/version.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("snapshot data v1"))
		})
	})

	// This test runs last (Ordered container) to verify noise data survived all CSI operations.
	Describe("Noise Data Integrity", func() {
		It("should verify all non-CSI noise data remains intact after CSI operations", func() {
			Expect(noiseVerifier).NotTo(BeNil(), "Noise verifier should be available")
			ctx := context.Background()

			By("Verifying noise parent dataset still exists")
			exists, err := noiseVerifier.DatasetExists(ctx, noiseParent)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue(), "Noise parent dataset %s should still exist", noiseParent)

			By("Verifying noise filesystem datasets still exist")
			fsParent := noiseParent + "/datasets"
			for i := 1; i <= actualDatasetCount; i++ {
				dsName := fmt.Sprintf("%s/ds-%03d", fsParent, i)
				exists, err := noiseVerifier.DatasetExists(ctx, dsName)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeTrue(), "Noise dataset %s should still exist", dsName)
			}

			By("Verifying noise zvols still exist")
			zvolParent := noiseParent + "/zvols"
			for i := 1; i <= actualZvolCount; i++ {
				zvolName := fmt.Sprintf("%s/zvol-%03d", zvolParent, i)
				exists, err := noiseVerifier.DatasetExists(ctx, zvolName)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeTrue(), "Noise zvol %s should still exist", zvolName)
			}

			By("Verifying noise NFS shares still exist")
			for i := 1; i <= nfsShareCount; i++ {
				sharePath := fmt.Sprintf("/mnt/%s/ds-%03d", fsParent, i)
				exists, err := noiseVerifier.NFSShareExists(ctx, sharePath)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeTrue(), "Noise NFS share for %s should still exist", sharePath)
			}

			By("All noise data verified intact")
			GinkgoWriter.Printf("Verified: %d datasets, %d zvols, %d NFS shares remain intact\n",
				actualDatasetCount, actualZvolCount, nfsShareCount)
		})
	})
})
