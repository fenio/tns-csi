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

var _ = Describe("Shared Encryption", func() {
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
		scParams   map[string]string
		name       string
		id         string
		accessMode corev1.PersistentVolumeAccessMode
		pvcTimeout time.Duration
		podTimeout time.Duration
	}

	getProtocols := func(f *framework.Framework) []protocolConfig {
		return []protocolConfig{
			{
				name:       "NFS",
				id:         "nfs",
				accessMode: corev1.ReadWriteMany,
				pvcTimeout: 2 * time.Minute,
				podTimeout: 2 * time.Minute,
				scParams: map[string]string{
					"protocol":              "nfs",
					"server":                f.Config.TrueNASHost,
					"pool":                  f.Config.TrueNASPool,
					"encryption":            "true",
					"encryptionGenerateKey": "true",
				},
			},
			{
				name:       "NVMe-oF",
				id:         "nvmeof",
				accessMode: corev1.ReadWriteOnce,
				pvcTimeout: 3 * time.Minute,
				podTimeout: 3 * time.Minute,
				scParams: map[string]string{
					"protocol":                  "nvmeof",
					"server":                    f.Config.TrueNASHost,
					"pool":                      f.Config.TrueNASPool,
					"transport":                 "tcp",
					"port":                      "4420",
					"csi.storage.k8s.io/fstype": "ext4",
					"encryption":                "true",
					"encryptionGenerateKey":     "true",
				},
			},
			{
				name:       "iSCSI",
				id:         "iscsi",
				accessMode: corev1.ReadWriteOnce,
				pvcTimeout: 3 * time.Minute,
				podTimeout: 3 * time.Minute,
				scParams: map[string]string{
					"protocol":                  "iscsi",
					"server":                    f.Config.TrueNASHost,
					"pool":                      f.Config.TrueNASPool,
					"port":                      "3260",
					"csi.storage.k8s.io/fstype": "ext4",
					"encryption":                "true",
					"encryptionGenerateKey":     "true",
				},
			},
		}
	}

	Context("Cross-Protocol Verification", func() {
		It("should provision encrypted volumes on both protocols", func() {
			ctx := context.Background()
			protocols := getProtocols(f)

			for _, proto := range protocols {
				By(fmt.Sprintf("Testing %s: Creating encrypted StorageClass", proto.name))
				scName := fmt.Sprintf("tns-csi-%s-encrypted-shared", proto.id)
				err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", proto.scParams)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteStorageClass(ctx, scName)
				})

				By(fmt.Sprintf("Testing %s: Creating PVC", proto.name))
				pvcName := "encrypted-shared-" + proto.id
				pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
					Name:             pvcName,
					StorageClassName: scName,
					Size:             "1Gi",
					AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
				})
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeletePVC(ctx, pvcName)
				})

				By(fmt.Sprintf("Testing %s: Waiting for PVC to be bound", proto.name))
				err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Creating pod", proto.name))
				podName := "encrypted-shared-pod-" + proto.id
				pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
					Name:      podName,
					PVCName:   pvc.Name,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeletePod(ctx, podName)
				})

				By(fmt.Sprintf("Testing %s: Waiting for pod to be ready", proto.name))
				err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Writing and reading data", proto.name))
				testData := fmt.Sprintf("Encrypted %s Test Data", proto.name)
				_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
					"sh", "-c", fmt.Sprintf("echo '%s' > /data/test.txt && sync", testData),
				})
				Expect(err).NotTo(HaveOccurred())

				output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(Equal(testData))

				By(fmt.Sprintf("Testing %s: Verified encrypted volume works correctly", proto.name))
			}
		})
	})

	Context("Snapshot Restore on Encrypted Volumes", func() {
		It("should create and restore snapshots on encrypted volumes [NFS]", func() {
			ctx := context.Background()
			protocols := getProtocols(f)
			proto := protocols[0] // NFS

			By("Creating encrypted StorageClass")
			scName := fmt.Sprintf("tns-csi-%s-encrypted-snap-restore", proto.id)
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", proto.scParams)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating VolumeSnapshotClass")
			snapshotClass := fmt.Sprintf("tns-csi-%s-encrypted-snap-class", proto.id)
			err = f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvcName := "encrypted-snap-source-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod and writing initial data")
			podName := "encrypted-snap-pod-" + proto.id
			pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, podName)
			})

			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			initialData := "Initial encrypted data for snapshot"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/snapshot-data.txt && sync", initialData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating snapshot")
			snapshotName := "encrypted-snapshot-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
			})

			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Writing additional data after snapshot")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", "echo 'Post-snapshot data' > /data/post-snapshot.txt && sync",
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating restored PVC from snapshot")
			restoredPVCName := "encrypted-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, scName, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, restoredPVCName)
			})

			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod to verify restored data")
			restoredPodName := "encrypted-restored-pod-" + proto.id
			restoredPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, restoredPodName)
			})

			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying restored data matches snapshot point-in-time")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/snapshot-data.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(initialData))

			By("Verifying post-snapshot data does NOT exist in restored volume")
			exists, err := f.K8s.FileExistsInPod(ctx, restoredPod.Name, "/data/post-snapshot.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "Post-snapshot data should not exist in restored volume")
		})

		It("should create and restore snapshots on encrypted volumes [NVMe-oF]", func() {
			ctx := context.Background()
			protocols := getProtocols(f)
			proto := protocols[1] // NVMe-oF

			By("Creating encrypted StorageClass")
			scName := fmt.Sprintf("tns-csi-%s-encrypted-snap-restore", proto.id)
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", proto.scParams)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating VolumeSnapshotClass")
			snapshotClass := fmt.Sprintf("tns-csi-%s-encrypted-snap-class", proto.id)
			err = f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvcName := "encrypted-snap-source-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod and writing initial data")
			podName := "encrypted-snap-pod-" + proto.id
			pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, podName)
			})

			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			initialData := "Initial encrypted NVMe-oF data for snapshot"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/snapshot-data.txt && sync", initialData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating snapshot")
			snapshotName := "encrypted-snapshot-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
			})

			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting source pod before restore (NVMe-oF ReadWriteOnce)")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating restored PVC from snapshot")
			restoredPVCName := "encrypted-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, scName, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, restoredPVCName)
			})

			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod to verify restored data")
			restoredPodName := "encrypted-restored-pod-" + proto.id
			restoredPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, restoredPodName)
			})

			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying restored data matches snapshot point-in-time")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/snapshot-data.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(initialData))
		})

		It("should create and restore snapshots on encrypted volumes [iSCSI]", func() {
			ctx := context.Background()
			protocols := getProtocols(f)
			proto := protocols[2] // iSCSI

			By("Creating encrypted StorageClass")
			scName := fmt.Sprintf("tns-csi-%s-encrypted-snap-restore", proto.id)
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", proto.scParams)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(ctx, scName)
			})

			By("Creating VolumeSnapshotClass")
			snapshotClass := fmt.Sprintf("tns-csi-%s-encrypted-snap-class", proto.id)
			err = f.K8s.CreateVolumeSnapshotClass(ctx, snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
			})

			By("Creating source PVC")
			pvcName := "encrypted-snap-source-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, pvcName)
			})

			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod and writing initial data")
			podName := "encrypted-snap-pod-" + proto.id
			pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, podName)
			})

			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			initialData := "Initial encrypted iSCSI data for snapshot"
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
				"sh", "-c", fmt.Sprintf("echo '%s' > /data/snapshot-data.txt && sync", initialData),
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating snapshot")
			snapshotName := "encrypted-snapshot-" + proto.id
			err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
			})

			err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting source pod before restore (iSCSI ReadWriteOnce)")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred())
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating restored PVC from snapshot")
			restoredPVCName := "encrypted-restored-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, scName, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, restoredPVCName)
			})

			err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, proto.pvcTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating pod to verify restored data")
			restoredPodName := "encrypted-restored-pod-" + proto.id
			restoredPod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
				Name:      restoredPodName,
				PVCName:   restoredPVCName,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePod(ctx, restoredPodName)
			})

			err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying restored data matches snapshot point-in-time")
			output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/snapshot-data.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(initialData))
		})
	})

	Context("Data Integrity", func() {
		It("should maintain data integrity with large files on encrypted volumes", func() {
			ctx := context.Background()
			protocols := getProtocols(f)

			for _, proto := range protocols {
				By(fmt.Sprintf("Testing %s: Creating encrypted StorageClass", proto.name))
				scName := fmt.Sprintf("tns-csi-%s-encrypted-integrity", proto.id)
				err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", proto.scParams)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteStorageClass(ctx, scName)
				})

				By(fmt.Sprintf("Testing %s: Creating PVC", proto.name))
				pvcName := "encrypted-integrity-" + proto.id
				pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
					Name:             pvcName,
					StorageClassName: scName,
					Size:             "2Gi",
					AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
				})
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeletePVC(ctx, pvcName)
				})

				err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Creating pod", proto.name))
				podName := "encrypted-integrity-pod-" + proto.id
				pod, err := f.K8s.CreatePod(ctx, framework.PodOptions{
					Name:      podName,
					PVCName:   pvc.Name,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeletePod(ctx, podName)
				})

				err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Writing 50MB random file", proto.name))
				_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
					"sh", "-c", "dd if=/dev/urandom of=/data/largefile.bin bs=1M count=50 2>/dev/null && sync",
				})
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Computing checksum", proto.name))
				checksum1, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"md5sum", "/data/largefile.bin"})
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("Testing %s: Re-reading and verifying checksum", proto.name))
				checksum2, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"md5sum", "/data/largefile.bin"})
				Expect(err).NotTo(HaveOccurred())
				Expect(checksum2).To(Equal(checksum1), "Checksum mismatch - data corruption detected")

				By(fmt.Sprintf("Testing %s: Verified data integrity on encrypted volume", proto.name))
			}
		})
	})
})
