// Package encryption contains E2E tests for ZFS native encryption support.
package encryption

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NFS Encryption", func() {
	var f *framework.Framework

	const (
		pvcTimeout = 2 * time.Minute
		podTimeout = 2 * time.Minute
	)

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

	Context("Basic Operations", func() {
		It("should provision encrypted volume and perform I/O", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass")
			scName := "tns-csi-nfs-encrypted-basic"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-basic",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-pod-basic",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing test data")
			testData := "Encrypted NFS Test Data"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Reading back test data")
			output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))

			By("Verifying binary data integrity")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", "dd if=/dev/urandom of=/data/random.bin bs=1M count=5 2>/dev/null && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			checksumBefore, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"md5sum", "/data/random.bin"})
			Expect(err).NotTo(HaveOccurred())

			checksumAfter, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"md5sum", "/data/random.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(checksumAfter).To(Equal(checksumBefore))
		})

		It("should provision encrypted volume with custom algorithm (AES-128-CCM)", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass with AES-128-CCM")
			scName := "tns-csi-nfs-encrypted-aes128"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionAlgorithm":   "AES-128-CCM",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-aes128",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod and verifying I/O")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-pod-aes128",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			testData := "AES-128-CCM Encrypted Data"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))
		})
	})

	Context("Volume Expansion", func() {
		It("should expand encrypted volume", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass")
			scName := "tns-csi-nfs-encrypted-expand"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-expand",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-pod-expand",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Writing initial data")
			testData := "Data before expansion"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Expanding PVC to 3Gi")
			err = f.K8s.ExpandPVC(ctx, pvc.Name, "3Gi")
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for expansion to complete")
			Eventually(func() string {
				capacity, _ := f.K8s.GetPVCCapacity(ctx, pvc.Name)
				return capacity
			}, 2*time.Minute, 5*time.Second).Should(Equal("3Gi"))

			By("Verifying data after expansion")
			output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))

			By("Writing large file to expanded space")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", "dd if=/dev/zero of=/data/bigfile bs=1M count=100 2>/dev/null && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"ls", "-la", "/data/bigfile"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("bigfile"))
		})
	})

	Context("Snapshots", func() {
		It("should create snapshot from encrypted volume and restore", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass")
			scName := "tns-csi-nfs-encrypted-snapshot"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating VolumeSnapshotClass")
			snapshotClass := "tns-csi-nfs-encrypted-snapshot-class"
			err = f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-snapshot-source",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod and writing data")
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-snapshot-pod",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			testData := "Snapshot Test Data"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/snapshot-test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating snapshot")
			snapshotName := "encrypted-nfs-snapshot"
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
			})

			By("Waiting for snapshot to be ready")
			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Creating restored PVC from snapshot")
			restoredPVCName := "encrypted-nfs-restored"
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, scName, "1Gi",
				[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
			Expect(err).NotTo(HaveOccurred())
			f.RegisterPVCCleanup(restoredPVCName)

			By("Waiting for restored PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod to verify restored data")
			restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-restored-pod",
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying restored data")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/snapshot-test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))
		})
	})

	Context("Volume Cloning", func() {
		It("should clone encrypted volume", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass")
			scName := "tns-csi-nfs-encrypted-clone"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating source PVC")
			sourcePVC, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-clone-source",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for source PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, sourcePVC.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating source pod and writing data")
			sourcePod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-clone-source-pod",
				PVCName:   sourcePVC.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, sourcePod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			testData := "Encrypted Clone Source Data"
			_, err = f.K8s.ExecInPod(ctx, sourcePod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/clone-test.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating clone PVC")
			clonePVCName := "encrypted-nfs-clone"
			err = f.K8s.CreatePVCFromPVC(ctx, clonePVCName, sourcePVC.Name, scName, "1Gi",
				[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})
			Expect(err).NotTo(HaveOccurred())
			f.RegisterPVCCleanup(clonePVCName)

			By("Waiting for clone PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, clonePVCName, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod to verify cloned data")
			clonePod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-clone-pod",
				PVCName:   clonePVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, clonePod.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying cloned data")
			output, err := f.K8s.ExecInPod(ctx, clonePod.Name, []string{"cat", "/data/clone-test.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))

			By("Verifying clone is independent (write to clone)")
			_, err = f.K8s.ExecInPod(ctx, clonePod.Name, []string{
				"sh", "-c", "echo 'Clone Only Data' > /data/clone-only.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying source doesn't have clone data")
			exists, err := f.K8s.FileExistsInPod(ctx, sourcePod.Name, "/data/clone-only.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Clone data should not appear in source")
		})
	})

	Context("Persistence", func() {
		It("should persist data across pod restart", func() {
			ctx := context.Background()

			By("Creating encrypted StorageClass")
			scName := "tns-csi-nfs-encrypted-persist"
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
				"protocol":              "nfs",
				"server":                f.Config.TrueNASHost,
				"pool":                  f.Config.TrueNASPool,
				"encryption":            "true",
				"encryptionGenerateKey": "true",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating PVC")
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "encrypted-nfs-persist",
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for PVC to be bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating first pod and writing data")
			pod1, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-persist-pod1",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, pod1.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			testData := "Persistent Encrypted Data"
			_, err = f.K8s.ExecInPod(ctx, pod1.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/persist.txt && sync", testData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Deleting first pod")
			err = f.K8s.DeletePod(ctx, pod1.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod1.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating second pod")
			pod2, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "encrypted-nfs-persist-pod2",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, pod2.Name, podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying data persisted")
			output, err := f.K8s.ExecInPod(ctx, pod2.Name, []string{"cat", "/data/persist.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(testData))
		})
	})
})
