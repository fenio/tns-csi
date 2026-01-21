// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("Snapshot Restore", func() {
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

	// Test parameters for each protocol
	type protocolConfig struct {
		name          string // Display name for test output
		id            string // Lowercase identifier for K8s resource names (RFC 1123)
		storageClass  string
		snapshotClass string
		accessMode    corev1.PersistentVolumeAccessMode
		podTimeout    time.Duration
		needsPodFirst bool // NVMe-oF uses WaitForFirstConsumer
	}

	protocols := []protocolConfig{
		{
			name:          "NFS",
			id:            "nfs",
			storageClass:  "tns-csi-nfs",
			snapshotClass: "tns-csi-nfs-snapshot",
			accessMode:    corev1.ReadWriteMany,
			podTimeout:    2 * time.Minute,
			needsPodFirst: false,
		},
		{
			name:          "NVMe-oF",
			id:            "nvmeof",
			storageClass:  "tns-csi-nvmeof",
			snapshotClass: "tns-csi-nvmeof-snapshot",
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
		{
			name:          "iSCSI",
			id:            "iscsi",
			storageClass:  "tns-csi-iscsi",
			snapshotClass: "tns-csi-iscsi-snapshot",
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
	}

	for _, proto := range protocols {
		It("should restore from multiple snapshots with correct point-in-time data ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, proto.snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), proto.snapshotClass)
			})

			By("Creating source PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "snapshot-restore-source-" + proto.id,
				StorageClassName: proto.storageClass,
				Size:             "2Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

			By("Creating source pod")
			podName := "snapshot-restore-source-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

			By("Waiting for source pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			if err != nil && f.Verbose() {
				// Capture diagnostics on failure (verbose mode only)
				GinkgoWriter.Printf("Pod failed to become ready, capturing diagnostics...\n")
				if podObj, getErr := f.K8s.GetPod(ctx, pod.Name); getErr == nil {
					GinkgoWriter.Printf("Pod status: %s\n", podObj.Status.Phase)
					for _, cond := range podObj.Status.Conditions {
						GinkgoWriter.Printf("  Condition %s: %s (reason: %s)\n", cond.Type, cond.Status, cond.Reason)
					}
					for _, cs := range podObj.Status.ContainerStatuses {
						GinkgoWriter.Printf("  Container %s: Ready=%v\n", cs.Name, cs.Ready)
						if cs.State.Waiting != nil {
							GinkgoWriter.Printf("    Waiting: %s - %s\n", cs.State.Waiting.Reason, cs.State.Waiting.Message)
						}
					}
				}
			}
			Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

			By("Waiting for source PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

			By("Writing version 1 data")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Version 1 data' > /data/version.txt && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to write version 1 data")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mkdir -p /data/v1 && for i in 1 2 3 4 5; do echo \"File $i version 1\" > /data/v1/file$i.txt; done && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to create v1 files")

			By("Verifying version 1 data")
			v1Data, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/version.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(v1Data).To(ContainSubstring("Version 1 data"))

			By("Creating first snapshot")
			snapshot1 := "snapshot-restore-1-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshot1, pvc.Name, proto.snapshotClass)
			Expect(err).NotTo(HaveOccurred(), "Failed to create first snapshot")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshot1)
			})

			By("Waiting for first snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshot1, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "First snapshot did not become ready")

			By("Writing version 2 data (modifying source)")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Version 2 data' > /data/version.txt && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to write version 2 data")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mkdir -p /data/v2 && for i in 1 2 3; do echo \"File $i version 2\" > /data/v2/file$i.txt; done && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to create v2 files")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Modified after snapshot 1' > /data/v1/modified.txt && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to add modified file")

			By("Creating second snapshot")
			snapshot2 := "snapshot-restore-2-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshot2, pvc.Name, proto.snapshotClass)
			Expect(err).NotTo(HaveOccurred(), "Failed to create second snapshot")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshot2)
			})

			By("Waiting for second snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshot2, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Second snapshot did not become ready")

			// ========== Restore from snapshot 1 ==========

			By("Restoring PVC from first snapshot")
			restore1PVC := "snapshot-restore-pvc-1-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restore1PVC, snapshot1, proto.storageClass, "2Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot 1")
			// Register cleanup with PV wait (restored PVCs are clones)
			f.RegisterPVCCleanup(restore1PVC)

			By("Creating pod for restored PVC 1")
			restore1PodName := "snapshot-restore-pod-1-" + proto.id
			restore1Pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restore1PodName,
				PVCName:   restore1PVC,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create pod for restore 1")

			By("Waiting for restore pod 1 to be ready")
			err = f.K8s.WaitForPodReady(ctx, restore1Pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Restore pod 1 did not become ready")

			By("Waiting for restored PVC 1 to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, restore1PVC, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restored PVC 1 did not become Bound")

			By("Verifying snapshot 1 data is restored")
			restore1Data, err := f.K8s.ExecInPod(ctx, restore1Pod.Name, []string{"cat", "/data/version.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read version from restore 1")
			Expect(restore1Data).To(ContainSubstring("Version 1 data"), "Snapshot 1 should have version 1 data")

			By("Verifying v2 directory does NOT exist in snapshot 1 restore")
			_, err = f.K8s.ExecInPod(ctx, restore1Pod.Name, []string{"ls", "/data/v2/"})
			Expect(err).To(HaveOccurred(), "v2 directory should NOT exist in snapshot 1")

			By("Verifying modified.txt does NOT exist in snapshot 1 restore")
			exists, err := f.K8s.FileExistsInPod(ctx, restore1Pod.Name, "/data/v1/modified.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "modified.txt should NOT exist in snapshot 1")

			// ========== Restore from snapshot 2 ==========

			By("Restoring PVC from second snapshot")
			restore2PVC := "snapshot-restore-pvc-2-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restore2PVC, snapshot2, proto.storageClass, "2Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot 2")
			// Register cleanup with PV wait (restored PVCs are clones)
			f.RegisterPVCCleanup(restore2PVC)

			By("Creating pod for restored PVC 2")
			restore2PodName := "snapshot-restore-pod-2-" + proto.id
			restore2Pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restore2PodName,
				PVCName:   restore2PVC,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create pod for restore 2")

			By("Waiting for restore pod 2 to be ready")
			err = f.K8s.WaitForPodReady(ctx, restore2Pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Restore pod 2 did not become ready")

			By("Waiting for restored PVC 2 to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, restore2PVC, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restored PVC 2 did not become Bound")

			By("Verifying snapshot 2 data is restored")
			restore2Data, err := f.K8s.ExecInPod(ctx, restore2Pod.Name, []string{"cat", "/data/version.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read version from restore 2")
			Expect(restore2Data).To(ContainSubstring("Version 2 data"), "Snapshot 2 should have version 2 data")

			By("Verifying v2 directory EXISTS in snapshot 2 restore")
			_, err = f.K8s.ExecInPod(ctx, restore2Pod.Name, []string{"ls", "/data/v2/"})
			Expect(err).NotTo(HaveOccurred(), "v2 directory should exist in snapshot 2")

			By("Verifying modified.txt EXISTS in snapshot 2 restore")
			exists, err = f.K8s.FileExistsInPod(ctx, restore2Pod.Name, "/data/v1/modified.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue(), "modified.txt should exist in snapshot 2")

			// ========== Verify volume independence ==========

			By("Verifying volumes are independent - writing to restore 1")
			_, err = f.K8s.ExecInPod(ctx, restore1Pod.Name, []string{"sh", "-c", "echo 'restore1 modification' > /data/restore1-only.txt && sync"})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying source volume does not have restore1 modification")
			exists, err = f.K8s.FileExistsInPod(ctx, pod.Name, "/data/restore1-only.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Source should not have restore1 modification")

			By("Verifying restore2 volume does not have restore1 modification")
			exists, err = f.K8s.FileExistsInPod(ctx, restore2Pod.Name, "/data/restore1-only.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Restore2 should not have restore1 modification")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] Snapshot restore test completed successfully\n", proto.name)
				GinkgoWriter.Printf("  - Snapshot 1: Version 1 data, no v2 directory, no modified.txt\n")
				GinkgoWriter.Printf("  - Snapshot 2: Version 2 data, v2 directory present, modified.txt present\n")
				GinkgoWriter.Printf("  - All restored volumes are independent\n")
			}
		})
	}
})
