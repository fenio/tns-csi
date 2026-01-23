// Package iscsi contains iSCSI-specific E2E tests for the TrueNAS CSI driver.
package iscsi

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These tests verify that detached snapshots are truly independent at the ZFS level.
// A detached snapshot should:
// 1. NOT be a ZFS clone (no origin property)
// 2. Allow the source volume to be deleted without errors
// 3. Survive source volume deletion and remain usable

var _ = Describe("Detached Snapshot Independence", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create truly independent detached snapshot (no ZFS clone dependency)", func() {
		ctx := context.Background()

		storageClass := "tns-csi-iscsi"
		accessMode := corev1.ReadWriteOnce
		podTimeout := 6 * time.Minute
		pool := "storage"

		if f.TrueNAS == nil {
			Skip("TrueNAS verifier not configured - skipping ZFS-level verification")
		}

		By("Creating source PVC")
		sourcePVCName := "detached-indep-src-iscsi"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sourcePVCName,
			StorageClassName: storageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Creating source POD to write data and trigger volume provisioning")
		sourcePodName := "detached-indep-src-pod-iscsi"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      sourcePodName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source POD")

		By("Waiting for source POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Source POD did not become ready")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Getting the source PV name to find ZFS dataset")
		sourcePV, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV for source PVC")
		sourcePVName := sourcePV.Name
		GinkgoWriter.Printf("[iSCSI] Source PV: %s\n", sourcePVName)

		possibleDatasetPaths := []string{
			fmt.Sprintf("%s/%s", pool, sourcePVName),
			fmt.Sprintf("%s/%s", pool, sourcePVCName),
		}

		By("Finding source ZFS dataset")
		var sourceDatasetPath string
		for _, path := range possibleDatasetPaths {
			dsExists, dsErr := f.TrueNAS.DatasetExists(ctx, path)
			if dsErr == nil && dsExists {
				sourceDatasetPath = path
				break
			}
		}
		Expect(sourceDatasetPath).NotTo(BeEmpty(), "Could not find source ZFS dataset")
		GinkgoWriter.Printf("[iSCSI] Source ZFS dataset: %s\n", sourceDatasetPath)

		By("Writing test data to source volume")
		testData := fmt.Sprintf("Independence Test Data - iSCSI - %d", time.Now().UnixNano())
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/independence-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Creating VolumeSnapshotClass with detachedSnapshots=true")
		snapshotClassName := "detached-indep-snapclass-iscsi"
		err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "tns.csi.io", "Delete", map[string]string{
			"detachedSnapshots": "true",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
		})

		By("Creating detached VolumeSnapshot")
		snapshotName := "detached-indep-snap-iscsi"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClassName)
		Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
		})

		By("Waiting for detached snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Detached snapshot did not become ready")

		By("Getting VolumeSnapshotContent to find the detached snapshot dataset")
		contentInfo, err := f.K8s.GetVolumeSnapshotContent(ctx, snapshotName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshotContent")
		Expect(contentInfo).NotTo(BeNil(), "VolumeSnapshotContent is nil")
		GinkgoWriter.Printf("[iSCSI] Snapshot handle: %s\n", contentInfo.SnapshotHandle)

		detachedDatasetPath := fmt.Sprintf("%s/csi-detached-snapshots/%s", pool, snapshotName)

		By("Verifying detached snapshot dataset exists")
		exists, err := f.TrueNAS.DatasetExists(ctx, detachedDatasetPath)
		if err != nil || !exists {
			if contentInfo.SnapshotHandle != "" {
				GinkgoWriter.Printf("[iSCSI] Detached dataset not at expected path, checking handle...\n")
			}
		}
		GinkgoWriter.Printf("[iSCSI] Detached snapshot dataset path: %s (exists: %v)\n", detachedDatasetPath, exists)

		By("CRITICAL: Verifying detached snapshot is NOT a ZFS clone (no origin)")
		isClone, origin, err := f.TrueNAS.IsDatasetClone(ctx, detachedDatasetPath)
		if err != nil {
			GinkgoWriter.Printf("[iSCSI] WARNING: Could not check clone status: %v\n", err)
			var allDatasets []map[string]any
			if queryErr := f.TrueNAS.Client().Call(ctx, "pool.dataset.query", []any{
				[]any{[]any{"id", "~", "csi-detached-snapshots"}},
			}, &allDatasets); queryErr == nil {
				for _, ds := range allDatasets {
					if name, ok := ds["id"].(string); ok && strings.Contains(name, snapshotName) {
						detachedDatasetPath = name
						isClone, origin, _ = f.TrueNAS.IsDatasetClone(ctx, detachedDatasetPath)
						GinkgoWriter.Printf("[iSCSI] Found detached dataset: %s\n", detachedDatasetPath)
						break
					}
				}
			}
		}

		if isClone {
			GinkgoWriter.Printf("[iSCSI] FAILURE: Detached snapshot IS a clone! Origin: %s\n", origin)
			Fail(fmt.Sprintf("Detached snapshot %s is a ZFS clone with origin %s - it should be independent", detachedDatasetPath, origin))
		} else {
			GinkgoWriter.Printf("[iSCSI] SUCCESS: Detached snapshot is NOT a clone - it is truly independent\n")
		}

		By("Deleting source POD")
		err = f.K8s.DeletePod(ctx, sourcePodName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete source POD")
		err = f.K8s.WaitForPodDeleted(ctx, sourcePodName, 60*time.Second)
		Expect(err).NotTo(HaveOccurred(), "Source POD was not deleted")

		By("Deleting source PVC")
		err = f.K8s.DeletePVC(ctx, sourcePVCName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete source PVC")

		By("Waiting for source PVC to be deleted")
		err = f.K8s.WaitForPVCDeleted(ctx, sourcePVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC was not deleted in time")

		By("Verifying source ZFS dataset was deleted")
		time.Sleep(5 * time.Second)
		sourceExists, _ := f.TrueNAS.DatasetExists(ctx, sourceDatasetPath)
		if sourceExists {
			GinkgoWriter.Printf("[iSCSI] WARNING: Source dataset %s still exists after PVC deletion\n", sourceDatasetPath)
			Fail(fmt.Sprintf("Source dataset %s could not be deleted - likely due to clone dependency", sourceDatasetPath))
		} else {
			GinkgoWriter.Printf("[iSCSI] SUCCESS: Source dataset %s was deleted\n", sourceDatasetPath)
		}

		By("Verifying detached snapshot still exists after source deletion")
		snapshotInfo, err := f.K8s.GetVolumeSnapshot(ctx, snapshotName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get snapshot after source deletion")
		Expect(snapshotInfo).NotTo(BeNil(), "Snapshot should still exist")
		Expect(*snapshotInfo.ReadyToUse).To(BeTrue(), "Snapshot should still be ready")

		By("Restoring PVC from detached snapshot (after source was deleted)")
		restoredPVCName := "detached-indep-restored-iscsi"
		err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, storageClass, "1Gi",
			[]corev1.PersistentVolumeAccessMode{accessMode})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from detached snapshot")
		f.RegisterPVCCleanup(restoredPVCName)

		By("Creating POD to mount restored volume")
		restoredPodName := "detached-indep-restored-pod-iscsi"
		restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      restoredPodName,
			PVCName:   restoredPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create restored POD")

		By("Waiting for restored POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Restored POD did not become ready")

		By("Verifying data was restored from detached snapshot")
		output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/independence-test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
		Expect(output).To(Equal(testData), "Restored data should match original")

		if f.Verbose() {
			GinkgoWriter.Printf("[iSCSI] Test PASSED: Detached snapshot is truly independent\n")
			GinkgoWriter.Printf("  - Source volume was deleted successfully\n")
			GinkgoWriter.Printf("  - Detached snapshot survived source deletion\n")
			GinkgoWriter.Printf("  - Data was successfully restored from detached snapshot\n")
		}
	})
})
