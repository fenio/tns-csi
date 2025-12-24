// Package nfs contains E2E tests for NFS protocol support.
package nfs

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NFS Snapshot Restore", func() {
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

	It("should restore from multiple snapshots with correct point-in-time data", func() {
		ctx := context.Background()
		snapshotClass := "tns-csi-nfs-snapshot"

		By("Creating VolumeSnapshotClass")
		err := f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
		})

		By("Creating source PVC")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "snapshot-restore-source",
			StorageClassName: "tns-csi-nfs",
			Size:             "2Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Creating source pod")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snapshot-restore-source-pod",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

		By("Waiting for source pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

		By("Writing version 1 data")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Version 1 data' > /data/version.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write version 1 data")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mkdir -p /data/v1 && for i in 1 2 3 4 5; do echo \"File $i version 1\" > /data/v1/file$i.txt; done"})
		Expect(err).NotTo(HaveOccurred(), "Failed to create v1 files")

		By("Verifying version 1 data")
		v1Data, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/version.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(v1Data).To(ContainSubstring("Version 1 data"))

		By("Creating first snapshot")
		snapshot1 := "snapshot-restore-1"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshot1, pvc.Name, snapshotClass)
		Expect(err).NotTo(HaveOccurred(), "Failed to create first snapshot")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshot1)
		})

		By("Waiting for first snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshot1, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "First snapshot did not become ready")

		By("Writing version 2 data (modifying source)")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Version 2 data' > /data/version.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write version 2 data")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "mkdir -p /data/v2 && for i in 1 2 3; do echo \"File $i version 2\" > /data/v2/file$i.txt; done"})
		Expect(err).NotTo(HaveOccurred(), "Failed to create v2 files")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Modified after snapshot 1' > /data/v1/modified.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to add modified file")

		By("Creating second snapshot")
		snapshot2 := "snapshot-restore-2"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshot2, pvc.Name, snapshotClass)
		Expect(err).NotTo(HaveOccurred(), "Failed to create second snapshot")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshot2)
		})

		By("Waiting for second snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshot2, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Second snapshot did not become ready")

		// ========== Restore from snapshot 1 ==========

		By("Restoring PVC from first snapshot")
		restore1PVC := "snapshot-restore-pvc-1"
		err = f.K8s.CreatePVCFromSnapshot(ctx, restore1PVC, snapshot1, "tns-csi-nfs", "2Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot 1")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), restore1PVC)
		})

		By("Waiting for restored PVC 1 to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, restore1PVC, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored PVC 1 did not become Bound")

		By("Creating pod for restored PVC 1")
		restore1Pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snapshot-restore-pod-1",
			PVCName:   restore1PVC,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod for restore 1")

		By("Waiting for restore pod 1 to be ready")
		err = f.K8s.WaitForPodReady(ctx, restore1Pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restore pod 1 did not become ready")

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
		restore2PVC := "snapshot-restore-pvc-2"
		err = f.K8s.CreatePVCFromSnapshot(ctx, restore2PVC, snapshot2, "tns-csi-nfs", "2Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot 2")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), restore2PVC)
		})

		By("Waiting for restored PVC 2 to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, restore2PVC, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored PVC 2 did not become Bound")

		By("Creating pod for restored PVC 2")
		restore2Pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snapshot-restore-pod-2",
			PVCName:   restore2PVC,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod for restore 2")

		By("Waiting for restore pod 2 to be ready")
		err = f.K8s.WaitForPodReady(ctx, restore2Pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restore pod 2 did not become ready")

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
		_, err = f.K8s.ExecInPod(ctx, restore1Pod.Name, []string{"sh", "-c", "echo 'restore1 modification' > /data/restore1-only.txt"})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying source volume does not have restore1 modification")
		exists, err = f.K8s.FileExistsInPod(ctx, pod.Name, "/data/restore1-only.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Source should not have restore1 modification")

		By("Verifying restore2 volume does not have restore1 modification")
		exists, err = f.K8s.FileExistsInPod(ctx, restore2Pod.Name, "/data/restore1-only.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Restore2 should not have restore1 modification")

		GinkgoWriter.Printf("Snapshot restore test completed successfully\n")
		GinkgoWriter.Printf("  - Snapshot 1: Version 1 data, no v2 directory, no modified.txt\n")
		GinkgoWriter.Printf("  - Snapshot 2: Version 2 data, v2 directory present, modified.txt present\n")
		GinkgoWriter.Printf("  - All restored volumes are independent\n")
	})
})

var _ = Describe("NFS Name Templating", func() {
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

	It("should create volumes with templated names from StorageClass parameters", func() {
		ctx := context.Background()
		scName := "tns-csi-nfs-name-template"

		By("Creating StorageClass with nameTemplate parameter")
		params := map[string]string{
			"protocol":     "nfs",
			"pool":         f.Config.TrueNASPool,
			"server":       f.Config.TrueNASHost,
			"nameTemplate": "{{ .PVCNamespace }}-{{ .PVCName }}",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with nameTemplate")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with templated StorageClass")
		pvcName := "name-template-test"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Verifying volume handle contains templated name")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")

		expectedPattern := fmt.Sprintf("%s-%s", f.Namespace(), pvcName)
		Expect(volumeHandle).To(ContainSubstring(expectedPattern),
			"Volume handle should contain templated name: %s", expectedPattern)

		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected pattern: %s\n", expectedPattern)

		By("Creating test pod to verify volume works")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "name-template-pod",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying I/O works on templated volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'test data' > /data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to volume")

		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from volume")
		Expect(output).To(ContainSubstring("test data"))
	})

	It("should create volumes with prefix and suffix from StorageClass parameters", func() {
		ctx := context.Background()
		scName := "tns-csi-nfs-prefix-suffix"

		By("Creating StorageClass with namePrefix and nameSuffix")
		params := map[string]string{
			"protocol":   "nfs",
			"pool":       f.Config.TrueNASPool,
			"server":     f.Config.TrueNASHost,
			"namePrefix": "prod-",
			"nameSuffix": "-data",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with prefix/suffix")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with prefix/suffix StorageClass")
		pvcName := "prefix-suffix-test"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Verifying volume handle contains prefix and suffix")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")

		GinkgoWriter.Printf("Volume handle with prefix/suffix: %s\n", volumeHandle)
		Expect(volumeHandle).To(ContainSubstring("prod-"), "Volume handle should contain prefix 'prod-'")
		Expect(volumeHandle).To(ContainSubstring("-data"), "Volume handle should contain suffix '-data'")
	})
})
