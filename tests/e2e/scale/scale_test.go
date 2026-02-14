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

	Describe("Volume with Snapshots", func() {
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

		It("should create a volume with snapshot and delete both cleanly", func() {
			ctx := context.Background()
			snapshotClass := "tns-csi-nfs-snapshot-scale-del"

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.DeferCleanup(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating a PVC amid noise data")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "scale-snapdel-pvc",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Getting the volume handle (dataset path) for later verification")
			pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			datasetPath, err := f.K8s.GetVolumeHandle(ctx, pvName)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("Volume handle (dataset path): %s\n", datasetPath)

			By("Creating a pod and writing data")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-snapdel-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'data before snapshot' > /data/test.txt && sync"})
			Expect(err).NotTo(HaveOccurred())

			By("Creating a snapshot amid noise data")
			snapshotName := "scale-snapdel-snap"
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the pod first (must unmount before PVC deletion)")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the snapshot")
			err = f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the PVC (triggers CSI DeleteVolume)")
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC deletion to complete")
			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PV deletion to complete (CSI DeleteVolume)")
			err = f.K8s.WaitForPVDeleted(ctx, pvName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the ZFS dataset was deleted from TrueNAS backend")
			Expect(f.TrueNAS).NotTo(BeNil(), "TrueNAS verifier should be available")
			time.Sleep(5 * time.Second) // Give TrueNAS a moment to finalize
			exists, err := f.TrueNAS.DatasetExists(ctx, datasetPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Dataset %s should be deleted from TrueNAS", datasetPath)

			By("Verifying NFS share was cleaned up")
			nfsSharePath := "/mnt/" + datasetPath
			shareExists, err := f.TrueNAS.NFSShareExists(ctx, nfsSharePath)
			Expect(err).NotTo(HaveOccurred())
			Expect(shareExists).To(BeFalse(), "NFS share for %s should be deleted", nfsSharePath)

			GinkgoWriter.Printf("SUCCESS: Volume + snapshot created and deleted cleanly amid noise data\n")
		})

		It("should create multiple volumes with snapshots and delete them all", func() {
			ctx := context.Background()
			snapshotClass := "tns-csi-nfs-snapshot-scale-multi"
			volumeCount := 3

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.DeferCleanup(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			type volumeInfo struct {
				pvcName      string
				pvName       string
				datasetPath  string
				podName      string
				snapshotName string
			}
			volumes := make([]volumeInfo, volumeCount)

			By(fmt.Sprintf("Creating %d volumes with snapshots", volumeCount))
			for i := range volumeCount {
				vi := &volumes[i]
				vi.pvcName = fmt.Sprintf("scale-multi-%d-pvc", i)
				vi.podName = fmt.Sprintf("scale-multi-%d-pod", i)
				vi.snapshotName = fmt.Sprintf("scale-multi-%d-snap", i)

				// Create PVC
				_, err := f.CreatePVC(ctx, framework.PVCOptions{
					Name:             vi.pvcName,
					StorageClassName: "tns-csi-nfs",
					Size:             "1Gi",
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create PVC %s", vi.pvcName)
			}

			By("Waiting for all PVCs to become Bound and capturing dataset paths")
			for i := range volumeCount {
				vi := &volumes[i]
				err := f.K8s.WaitForPVCBound(ctx, vi.pvcName, 2*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "PVC %s did not become Bound", vi.pvcName)

				vi.pvName, err = f.K8s.GetPVName(ctx, vi.pvcName)
				Expect(err).NotTo(HaveOccurred())
				vi.datasetPath, err = f.K8s.GetVolumeHandle(ctx, vi.pvName)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("Volume %d: PV=%s, dataset=%s\n", i, vi.pvName, vi.datasetPath)
			}

			By("Creating pods and writing data to each volume")
			for i := range volumeCount {
				vi := &volumes[i]
				_, err := f.CreatePod(ctx, framework.PodOptions{
					Name:      vi.podName,
					PVCName:   vi.pvcName,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPodReady(ctx, vi.podName, 2*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				_, err = f.K8s.ExecInPod(ctx, vi.podName, []string{"sh", "-c",
					fmt.Sprintf("echo 'volume %d data' > /data/test.txt && sync", i)})
				Expect(err).NotTo(HaveOccurred())
			}

			By("Creating snapshots for each volume")
			for i := range volumeCount {
				vi := &volumes[i]
				err := f.K8s.CreateVolumeSnapshot(ctx, vi.snapshotName, vi.pvcName, snapshotClass)
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForSnapshotReady(ctx, vi.snapshotName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())
			}

			By("Deleting all pods")
			for i := range volumeCount {
				err := f.K8s.DeletePod(ctx, volumes[i].podName)
				Expect(err).NotTo(HaveOccurred())
			}
			for i := range volumeCount {
				err := f.K8s.WaitForPodDeleted(ctx, volumes[i].podName, 2*time.Minute)
				Expect(err).NotTo(HaveOccurred())
			}

			By("Deleting all snapshots")
			for i := range volumeCount {
				err := f.K8s.DeleteVolumeSnapshot(ctx, volumes[i].snapshotName)
				Expect(err).NotTo(HaveOccurred())
			}

			By("Deleting all PVCs")
			for i := range volumeCount {
				err := f.K8s.DeletePVC(ctx, volumes[i].pvcName)
				Expect(err).NotTo(HaveOccurred())
			}

			By("Waiting for all PVCs and PVs to be fully deleted")
			for i := range volumeCount {
				vi := &volumes[i]
				err := f.K8s.WaitForPVCDeleted(ctx, vi.pvcName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "PVC %s was not deleted in time", vi.pvcName)
				err = f.K8s.WaitForPVDeleted(ctx, vi.pvName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "PV %s was not deleted in time", vi.pvName)
			}

			By("Verifying all ZFS datasets were cleaned up from TrueNAS backend")
			Expect(f.TrueNAS).NotTo(BeNil())
			time.Sleep(5 * time.Second)
			for i := range volumeCount {
				vi := &volumes[i]
				exists, err := f.TrueNAS.DatasetExists(ctx, vi.datasetPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeFalse(), "Dataset %s should be deleted", vi.datasetPath)
			}

			GinkgoWriter.Printf("SUCCESS: %d volumes with snapshots created and deleted amid noise data\n", volumeCount)
		})

		It("should create a volume, snapshot, restore, delete restored, then delete original", func() {
			ctx := context.Background()
			snapshotClass := "tns-csi-nfs-snapshot-scale-restore"

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.DeferCleanup(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "scale-restore-src-pvc",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			srcPVName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			srcDatasetPath, err := f.K8s.GetVolumeHandle(ctx, srcPVName)
			Expect(err).NotTo(HaveOccurred())

			By("Writing data to source volume")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-restore-src-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'original data' > /data/version.txt && sync"})
			Expect(err).NotTo(HaveOccurred())

			By("Creating a snapshot")
			snapshotName := "scale-restore-snap"
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Restoring a new PVC from snapshot")
			restorePVCName := "scale-restore-dst-pvc"
			err = f.K8s.CreatePVCFromSnapshot(ctx, restorePVCName, snapshotName, "tns-csi-nfs", "1Gi",
				[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVCBound(ctx, restorePVCName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			restorePVName, err := f.K8s.GetPVName(ctx, restorePVCName)
			Expect(err).NotTo(HaveOccurred())
			restoreDatasetPath, err := f.K8s.GetVolumeHandle(ctx, restorePVName)
			Expect(err).NotTo(HaveOccurred())

			By("Mounting restored volume and verifying data")
			restorePod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "scale-restore-dst-pod",
				PVCName:   restorePVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodReady(ctx, restorePod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, restorePod.Name, []string{"cat", "/data/version.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("original data"))

			By("Deleting restored pod and PVC")
			err = f.K8s.DeletePod(ctx, restorePod.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, restorePod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.DeletePVC(ctx, restorePVCName)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVCDeleted(ctx, restorePVCName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVDeleted(ctx, restorePVName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting snapshot")
			err = f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting source pod and PVC")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both datasets are gone from TrueNAS")
			Expect(f.TrueNAS).NotTo(BeNil())
			time.Sleep(5 * time.Second)

			exists, err := f.TrueNAS.DatasetExists(ctx, srcDatasetPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Source dataset %s should be deleted", srcDatasetPath)

			exists, err = f.TrueNAS.DatasetExists(ctx, restoreDatasetPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Restored dataset %s should be deleted", restoreDatasetPath)

			GinkgoWriter.Printf("SUCCESS: Volume -> snapshot -> restore -> delete all, amid noise data\n")
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

			GinkgoWriter.Printf("Verified: %d datasets, %d zvols, %d NFS shares remain intact\n",
				actualDatasetCount, actualZvolCount, nfsShareCount)
		})
	})
})
