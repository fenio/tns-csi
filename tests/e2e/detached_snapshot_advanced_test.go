// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These tests cover advanced detached snapshot scenarios:
// 1. Detached snapshots via zfs send/receive (VolumeSnapshotClass with detachedSnapshots=true)
// 2. Restoring from detached snapshots
// 3. Detached snapshots surviving source volume deletion (DR scenario)
//
// Note: This is different from "detached clones" (StorageClass with detached=true)
// which are tested in nfs/nvmeof detached_snapshot_test.go files.

var _ = Describe("Detached Snapshot Advanced", func() {
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

	// Test parameters for each protocol
	type protocolConfig struct {
		name          string
		id            string
		storageClass  string
		accessMode    corev1.PersistentVolumeAccessMode
		podTimeout    time.Duration
		needsPodFirst bool
	}

	protocols := []protocolConfig{
		{
			name:          "NFS",
			id:            "nfs",
			storageClass:  "tns-csi-nfs",
			accessMode:    corev1.ReadWriteMany,
			podTimeout:    2 * time.Minute,
			needsPodFirst: false,
		},
		{
			name:          "NVMe-oF",
			id:            "nvmeof",
			storageClass:  "tns-csi-nvmeof",
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
	}

	for _, proto := range protocols {
		It("should create detached snapshot via zfs send/receive and restore from it ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating source PVC")
			sourcePVCName := "detached-snap-source-" + proto.id
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             sourcePVCName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

			By("Creating source pod to write data")
			sourcePodName := "detached-snap-source-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      sourcePodName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

			By("Waiting for source pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

			By("Waiting for source PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

			By("Writing test data to source volume")
			testData := fmt.Sprintf("Detached Snapshot Data - %s - %d", proto.name, time.Now().UnixNano())
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/detached-test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

			By("Creating VolumeSnapshotClass with detachedSnapshots=true (zfs send/receive)")
			snapshotClassName := "detached-snapclass-" + proto.id
			err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "tns.csi.io", "Delete", map[string]string{
				"detachedSnapshots": "true", // Creates independent dataset copy via zfs send/receive
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
			})

			By("Creating detached VolumeSnapshot")
			snapshotName := "detached-snap-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClassName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
			})

			By("Waiting for detached snapshot to be ready")
			// Detached snapshots take longer as they perform zfs send/receive
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Detached snapshot did not become ready")

			By("Restoring PVC from detached snapshot")
			restoredPVCName := "detached-snap-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, proto.storageClass, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from detached snapshot")
			f.RegisterPVCCleanup(restoredPVCName)

			By("Creating pod to mount restored volume")
			restoredPodName := "detached-snap-restored-pod-" + proto.id
			restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create restored pod")

			By("Waiting for restored pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Restored pod did not become ready")

			By("Waiting for restored PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restored PVC did not become Bound")

			By("Verifying data was restored from detached snapshot")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/detached-test.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
			Expect(output).To(Equal(testData), "Restored data should match original")

			GinkgoWriter.Printf("[%s] Successfully created and restored from detached snapshot\n", proto.name)
		})

		It("should preserve detached snapshot after source volume deletion ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating source PVC")
			sourcePVCName := "detached-dr-source-" + proto.id
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             sourcePVCName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

			By("Creating source pod to write data")
			sourcePodName := "detached-dr-source-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      sourcePodName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

			By("Waiting for source pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

			By("Waiting for source PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

			By("Writing test data to source volume")
			testData := fmt.Sprintf("DR Test Data - %s - %d", proto.name, time.Now().UnixNano())
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/dr-test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

			By("Creating VolumeSnapshotClass with detachedSnapshots=true")
			snapshotClassName := "detached-dr-snapclass-" + proto.id
			err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "tns.csi.io", "Delete", map[string]string{
				"detachedSnapshots": "true",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
			})

			By("Creating detached VolumeSnapshot")
			snapshotName := "detached-dr-snap-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClassName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
			})

			By("Waiting for detached snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Detached snapshot did not become ready")

			By("Deleting source pod")
			err = f.K8s.DeletePod(ctx, sourcePodName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete source pod")
			err = f.K8s.WaitForPodDeleted(ctx, sourcePodName, 60*time.Second)
			Expect(err).NotTo(HaveOccurred(), "Source pod was not deleted")

			By("Deleting source PVC (this would delete regular snapshots but not detached)")
			err = f.K8s.DeletePVC(ctx, sourcePVCName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete source PVC")
			err = f.K8s.WaitForPVCDeleted(ctx, sourcePVCName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Source PVC was not deleted")

			By("Waiting a moment for any cascading effects")
			time.Sleep(5 * time.Second)

			By("Verifying detached snapshot still exists and is ready")
			snapshotInfo, err := f.K8s.GetVolumeSnapshot(ctx, snapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to get snapshot after source deletion")
			Expect(snapshotInfo).NotTo(BeNil(), "Snapshot should still exist")
			Expect(snapshotInfo.ReadyToUse).NotTo(BeNil(), "Snapshot should have ReadyToUse status")
			Expect(*snapshotInfo.ReadyToUse).To(BeTrue(), "Snapshot should still be ready")

			By("Restoring PVC from detached snapshot (after source was deleted)")
			restoredPVCName := "detached-dr-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, proto.storageClass, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from detached snapshot")
			f.RegisterPVCCleanup(restoredPVCName)

			By("Creating pod to mount restored volume")
			restoredPodName := "detached-dr-restored-pod-" + proto.id
			restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create restored pod")

			By("Waiting for restored pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Restored pod did not become ready")

			By("Waiting for restored PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restored PVC did not become Bound")

			By("Verifying data was restored from detached snapshot after source deletion")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/dr-test.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
			Expect(output).To(Equal(testData), "Restored data should match original even after source deletion")

			GinkgoWriter.Printf("[%s] Successfully restored from detached snapshot after source volume deletion (DR scenario)\n", proto.name)
		})
	}
})
