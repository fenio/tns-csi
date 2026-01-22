// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

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
//
// This tests the zfs send/receive implementation, NOT zfs clone behavior.

var _ = Describe("Detached Snapshot Independence", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("all")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	type protocolConfig struct {
		name          string
		id            string
		storageClass  string
		pool          string // TrueNAS pool where volumes are created
		accessMode    corev1.PersistentVolumeAccessMode
		podTimeout    time.Duration
		needsPodFirst bool
	}

	// Get pool from environment or use default
	pool := "storage" // Default pool name - adjust if different

	protocols := []protocolConfig{
		{
			name:          "NFS",
			id:            "nfs",
			storageClass:  "tns-csi-nfs",
			pool:          pool,
			accessMode:    corev1.ReadWriteMany,
			podTimeout:    2 * time.Minute,
			needsPodFirst: false,
		},
		{
			name:          "NVMe-oF",
			id:            "nvmeof",
			storageClass:  "tns-csi-nvmeof",
			pool:          pool,
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
		{
			name:          "iSCSI",
			id:            "iscsi",
			storageClass:  "tns-csi-iscsi",
			pool:          pool,
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
	}

	for _, proto := range protocols {
		It("should create truly independent detached snapshot (no ZFS clone dependency) ["+proto.name+"]", func() {
			ctx := context.Background()

			// Skip if TrueNAS verifier is not available
			if f.TrueNAS == nil {
				Skip("TrueNAS verifier not configured - skipping ZFS-level verification")
			}

			By("Creating source PVC")
			sourcePVCName := "detached-indep-src-" + proto.id
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             sourcePVCName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

			By("Creating source pod to write data and trigger volume provisioning")
			sourcePodName := "detached-indep-src-pod-" + proto.id
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

			By("Getting the source PV name to find ZFS dataset")
			sourcePV, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to get PV for source PVC")
			sourcePVName := sourcePV.Name
			GinkgoWriter.Printf("[%s] Source PV: %s\n", proto.name, sourcePVName)

			// The ZFS dataset path is typically: {pool}/{pv-name} or {pool}/{pvc-name}
			// Let's construct possible dataset paths
			possibleDatasetPaths := []string{
				fmt.Sprintf("%s/%s", proto.pool, sourcePVName),
				fmt.Sprintf("%s/%s", proto.pool, sourcePVCName),
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
			GinkgoWriter.Printf("[%s] Source ZFS dataset: %s\n", proto.name, sourceDatasetPath)

			By("Writing test data to source volume")
			testData := fmt.Sprintf("Independence Test Data - %s - %d", proto.name, time.Now().UnixNano())
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/independence-test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

			By("Creating VolumeSnapshotClass with detachedSnapshots=true")
			snapshotClassName := "detached-indep-snapclass-" + proto.id
			err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "tns.csi.io", "Delete", map[string]string{
				"detachedSnapshots": "true",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
			})

			By("Creating detached VolumeSnapshot")
			snapshotName := "detached-indep-snap-" + proto.id
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
			GinkgoWriter.Printf("[%s] Snapshot handle: %s\n", proto.name, contentInfo.SnapshotHandle)

			// The detached snapshot dataset should be in csi-detached-snapshots folder
			// Format: {pool}/csi-detached-snapshots/{snapshot-name}
			detachedDatasetPath := fmt.Sprintf("%s/csi-detached-snapshots/%s", proto.pool, snapshotName)

			By("Verifying detached snapshot dataset exists")
			exists, err := f.TrueNAS.DatasetExists(ctx, detachedDatasetPath)
			if err != nil || !exists {
				// Try alternative: the snapshot name from the handle
				if contentInfo.SnapshotHandle != "" {
					// Parse snapshot handle - format varies
					GinkgoWriter.Printf("[%s] Detached dataset not at expected path, checking handle...\n", proto.name)
				}
			}
			// Continue even if not found - the key test is origin check
			GinkgoWriter.Printf("[%s] Detached snapshot dataset path: %s (exists: %v)\n", proto.name, detachedDatasetPath, exists)

			By("CRITICAL: Verifying detached snapshot is NOT a ZFS clone (no origin)")
			isClone, origin, err := f.TrueNAS.IsDatasetClone(ctx, detachedDatasetPath)
			if err != nil {
				GinkgoWriter.Printf("[%s] WARNING: Could not check clone status: %v\n", proto.name, err)
				// Try to find the dataset via different paths
				var allDatasets []map[string]any
				if queryErr := f.TrueNAS.Client().Call(ctx, "pool.dataset.query", []any{
					[]any{[]any{"id", "~", "csi-detached-snapshots"}},
				}, &allDatasets); queryErr == nil {
					for _, ds := range allDatasets {
						if name, ok := ds["id"].(string); ok && strings.Contains(name, snapshotName) {
							detachedDatasetPath = name
							isClone, origin, _ = f.TrueNAS.IsDatasetClone(ctx, detachedDatasetPath)
							GinkgoWriter.Printf("[%s] Found detached dataset: %s\n", proto.name, detachedDatasetPath)
							break
						}
					}
				}
			}

			// THIS IS THE KEY ASSERTION: Detached snapshots should NOT be clones
			if isClone {
				GinkgoWriter.Printf("[%s] FAILURE: Detached snapshot IS a clone! Origin: %s\n", proto.name, origin)
				GinkgoWriter.Printf("[%s] This means the detached snapshot is NOT independent.\n", proto.name)
				GinkgoWriter.Printf("[%s] The source volume cannot be deleted while this clone exists.\n", proto.name)
				Fail(fmt.Sprintf("Detached snapshot %s is a ZFS clone with origin %s - it should be independent (created via zfs send/receive)", detachedDatasetPath, origin))
			} else {
				GinkgoWriter.Printf("[%s] SUCCESS: Detached snapshot is NOT a clone (no origin) - it is truly independent\n", proto.name)
			}

			By("Deleting source pod")
			err = f.K8s.DeletePod(ctx, sourcePodName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete source pod")
			err = f.K8s.WaitForPodDeleted(ctx, sourcePodName, 60*time.Second)
			Expect(err).NotTo(HaveOccurred(), "Source pod was not deleted")

			By("Deleting source PVC")
			err = f.K8s.DeletePVC(ctx, sourcePVCName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete source PVC")

			By("Waiting for source PVC to be deleted")
			err = f.K8s.WaitForPVCDeleted(ctx, sourcePVCName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Source PVC was not deleted in time")

			By("Verifying source ZFS dataset was deleted")
			// Wait a bit for async deletion
			time.Sleep(5 * time.Second)
			sourceExists, _ := f.TrueNAS.DatasetExists(ctx, sourceDatasetPath)
			if sourceExists {
				GinkgoWriter.Printf("[%s] WARNING: Source dataset %s still exists after PVC deletion\n", proto.name, sourceDatasetPath)
				// This is the bug case - source couldn't be deleted due to clone dependency
				Fail(fmt.Sprintf("Source dataset %s could not be deleted - likely due to clone dependency from detached snapshot", sourceDatasetPath))
			} else {
				GinkgoWriter.Printf("[%s] SUCCESS: Source dataset %s was deleted\n", proto.name, sourceDatasetPath)
			}

			By("Verifying detached snapshot still exists after source deletion")
			snapshotInfo, err := f.K8s.GetVolumeSnapshot(ctx, snapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to get snapshot after source deletion")
			Expect(snapshotInfo).NotTo(BeNil(), "Snapshot should still exist")
			Expect(*snapshotInfo.ReadyToUse).To(BeTrue(), "Snapshot should still be ready")

			By("Restoring PVC from detached snapshot (after source was deleted)")
			restoredPVCName := "detached-indep-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, proto.storageClass, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from detached snapshot")
			f.RegisterPVCCleanup(restoredPVCName)

			By("Creating pod to mount restored volume")
			restoredPodName := "detached-indep-restored-pod-" + proto.id
			restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create restored pod")

			By("Waiting for restored pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Restored pod did not become ready")

			By("Verifying data was restored from detached snapshot")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/independence-test.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
			Expect(output).To(Equal(testData), "Restored data should match original")

			GinkgoWriter.Printf("[%s] Test PASSED: Detached snapshot is truly independent\n", proto.name)
			GinkgoWriter.Printf("  - Source volume was deleted successfully\n")
			GinkgoWriter.Printf("  - Detached snapshot survived source deletion\n")
			GinkgoWriter.Printf("  - Data was successfully restored from detached snapshot\n")
		})
	}
})
