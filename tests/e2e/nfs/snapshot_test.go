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

var _ = Describe("NFS Snapshots", func() {
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

	It("should create a snapshot, restore from it, and verify data", func() {
		ctx := context.Background()
		snapshotClassName := "tns-csi-snapshot-nfs"
		snapshotName := "test-snapshot-nfs"

		By("Creating VolumeSnapshotClass")
		err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClassName, "tns.csi.io", "Delete")
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.DeferCleanup(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClassName)
		})

		By("Creating source PVC")
		sourcePVC, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "snapshot-source-pvc",
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, sourcePVC.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Creating source pod")
		sourcePod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snapshot-source-pod",
			PVCName:   sourcePVC.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

		By("Waiting for source pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, sourcePod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

		By("Writing test data to the source volume")
		_, err = f.K8s.ExecInPod(ctx, sourcePod.Name, []string{"sh", "-c", "echo 'Snapshot Test Data' > /data/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Writing a large file for integrity verification")
		_, err = f.K8s.ExecInPod(ctx, sourcePod.Name, []string{"sh", "-c", "dd if=/dev/urandom of=/data/iotest.bin bs=1M count=10 && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write large file")

		By("Creating VolumeSnapshot")
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, sourcePVC.Name, snapshotClassName)
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")
		f.DeferCleanup(func() error {
			return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
		})

		By("Waiting for snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Snapshot did not become ready")

		By("Creating PVC from snapshot")
		err = f.K8s.CreatePVCFromSnapshot(ctx, "pvc-from-snapshot", snapshotName, "tns-csi-nfs", "1Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot")
		f.DeferCleanup(func() error {
			return f.K8s.DeletePVC(context.Background(), "pvc-from-snapshot")
		})

		By("Waiting for restored PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, "pvc-from-snapshot", 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored PVC did not become Bound")

		By("Creating pod to mount restored volume")
		restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snapshot-restored-pod",
			PVCName:   "pvc-from-snapshot",
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create restored pod")

		By("Waiting for restored pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored pod did not become ready")

		By("Verifying test data was restored from snapshot")
		output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data from restored volume")
		Expect(output).To(Equal("Snapshot Test Data"), "Data mismatch after restore")

		By("Verifying large file was restored")
		exists, err := f.K8s.FileExistsInPod(ctx, restoredPod.Name, "/data/iotest.bin")
		Expect(err).NotTo(HaveOccurred(), "Failed to check file existence")
		Expect(exists).To(BeTrue(), "Large file not found after restore")

		By("Writing new data to restored volume (verify it's writable)")
		_, err = f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"sh", "-c", "echo 'New data on restored volume' > /data/restored.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to restored volume")

		By("Verifying new data was written")
		output, err = f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/restored.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read new data")
		Expect(output).To(Equal("New data on restored volume"))
	})
})
