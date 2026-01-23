package iscsi_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Detached Snapshot", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts for iSCSI operations (longer due to block device setup)
	const (
		pvcTimeout      = 180 * time.Second
		podTimeout      = 180 * time.Second
		snapshotTimeout = 120 * time.Second
		deleteTimeout   = 60 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())

		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create detached clone that survives snapshot deletion", func() {
		// NOTE: Cleanup is LIFO (Last In, First Out). For ZFS clone dependencies,
		// the detached PVC (clone) must be deleted BEFORE the source PVC.
		// Registration order: source PVC first, then detached PVC, so cleanup deletes detached first.

		By("Creating a source PVC")
		sourcePVCName := "source-pvc"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sourcePVCName,
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		// Note: f.CreatePVC already registers cleanup with PV wait - no manual cleanup needed

		By("Creating a POD to write test data (triggers WaitForFirstConsumer binding)")
		sourcePodName := "source-pod"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      sourcePodName,
			PVCName:   sourcePVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())
		// Note: f.CreatePod already registers cleanup - no manual cleanup needed

		By("Waiting for source POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, sourcePodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for source PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, sourcePVCName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test data to the volume")
		testData := "ISCSI-CSI-DETACHED-PATTERN"
		_, err = f.K8s.ExecInPod(ctx, sourcePodName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/test-pattern.txt", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Syncing filesystem to ensure data is persisted")
		_, err = f.K8s.ExecInPod(ctx, sourcePodName, []string{"sync"})
		Expect(err).NotTo(HaveOccurred())

		By("Creating VolumeSnapshotClass")
		snapshotClassName := "test-snapshot-class-iscsi"
		err = f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClassName, "tns.csi.io", "Delete")
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
		})

		By("Creating a VolumeSnapshot")
		snapshotName := "test-snapshot"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, sourcePVCName, snapshotClassName)
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
		})

		By("Waiting for snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, snapshotTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Creating detached StorageClass (detached=true promotes clones to be independent)")
		detachedStorageClass := "tns-csi-iscsi-detached"
		err = f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, detachedStorageClass, "tns.csi.io", map[string]string{
			"protocol": "iscsi",
			"server":   f.Config.TrueNASHost,
			"pool":     f.Config.TrueNASPool,
			"port":     "3260",
			"fsType":   "ext4",
			"detached": "true", // Promotes clones to be independent from source snapshot
		}, "WaitForFirstConsumer")
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, detachedStorageClass)
		})

		By("Creating a detached PVC from the snapshot")
		detachedPVCName := "detached-pvc"
		err = f.K8s.CreatePVCFromSnapshot(ctx, detachedPVCName, snapshotName, detachedStorageClass, "1Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
		Expect(err).NotTo(HaveOccurred())
		// Register cleanup with PV wait (clone must be fully deleted before source)
		f.RegisterPVCCleanup(detachedPVCName)

		By("Creating a POD to mount the detached clone (triggers WaitForFirstConsumer binding)")
		detachedPodName := "detached-pod"
		detachedPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      detachedPodName,
			PVCName:   detachedPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(detachedPod).NotTo(BeNil())
		// Note: f.CreatePod already registers cleanup - no manual cleanup needed

		By("Waiting for detached POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, detachedPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for detached PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, detachedPVCName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying data is present in detached clone")
		output, err := f.K8s.ExecInPod(ctx, detachedPodName, []string{"cat", "/data/test-pattern.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData), "Detached clone should contain the original data")

		By("Writing new data to detached clone")
		detachedData := "Data written to detached clone"
		_, err = f.K8s.ExecInPod(ctx, detachedPodName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/detached.txt && sync", detachedData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the source POD first")
		err = f.K8s.DeletePod(ctx, sourcePodName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, sourcePodName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the original snapshot")
		err = f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting a moment for any cascading effects")
		time.Sleep(5 * time.Second)

		By("Verifying detached POD is still running")
		pod, err = f.K8s.GetPod(ctx, detachedPodName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "Detached POD should still be running")

		By("Verifying original data is still accessible")
		output, err = f.K8s.ExecInPod(ctx, detachedPodName, []string{"cat", "/data/test-pattern.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(testData), "Original data should still be accessible")

		By("Verifying detached clone data is still accessible")
		output, err = f.K8s.ExecInPod(ctx, detachedPodName, []string{"cat", "/data/detached.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(detachedData), "Detached clone data should still be accessible")

		By("Writing new data after snapshot deletion")
		postDeleteData := "Data written after snapshot deletion"
		_, err = f.K8s.ExecInPod(ctx, detachedPodName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/post-delete.txt && sync", postDeleteData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying write after snapshot deletion succeeded")
		output, err = f.K8s.ExecInPod(ctx, detachedPodName, []string{"cat", "/data/post-delete.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(postDeleteData), "Should be able to write after snapshot deletion")
	})
})
