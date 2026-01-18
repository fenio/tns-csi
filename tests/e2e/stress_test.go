// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("Snapshot Stress", func() {
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
	}

	protocols := []protocolConfig{
		{
			name:          "NFS",
			id:            "nfs",
			storageClass:  "tns-csi-nfs",
			snapshotClass: "tns-csi-nfs-snapshot-stress",
			accessMode:    corev1.ReadWriteMany,
			podTimeout:    2 * time.Minute,
		},
		{
			name:          "NVMe-oF",
			id:            "nvmeof",
			storageClass:  "tns-csi-nvmeof",
			snapshotClass: "tns-csi-nvmeof-snapshot-stress",
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
		},
		{
			name:          "iSCSI",
			id:            "iscsi",
			storageClass:  "tns-csi-iscsi",
			snapshotClass: "tns-csi-iscsi-snapshot-stress",
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
		},
	}

	for _, proto := range protocols {
		It("should handle multiple snapshots of the same volume ["+proto.name+"]", func() {
			ctx := context.Background()
			numSnapshots := 3

			By("Creating VolumeSnapshotClass")
			err := f.K8s.CreateVolumeSnapshotClass(ctx, proto.snapshotClass, "tns.csi.io", "Delete")
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteVolumeSnapshotClass(context.Background(), proto.snapshotClass)
			})

			By("Creating source PVC")
			pvcName := "snapshot-stress-source-" + proto.id
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

			By("Creating source pod")
			podName := "snapshot-stress-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
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

			By("Writing initial data")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'Initial Data' > /data/initial.txt && sync"})
			Expect(err).NotTo(HaveOccurred(), "Failed to write initial data")

			snapshotNames := make([]string, numSnapshots)

			By(fmt.Sprintf("Creating %d snapshots with unique data each", numSnapshots))
			for i := range numSnapshots {
				snapshotName := fmt.Sprintf("snapshot-stress-%d-%s", i+1, proto.id)
				snapshotNames[i] = snapshotName

				// Write unique data before each snapshot
				_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c",
					fmt.Sprintf("echo 'Snapshot %d Data' > /data/snapshot-%d.txt && sync", i+1, i+1)})
				Expect(err).NotTo(HaveOccurred(), "Failed to write data for snapshot %d", i+1)

				// Create snapshot
				err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, proto.snapshotClass)
				Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot %d", i+1)
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
				})

				// Wait for snapshot to be ready
				err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Snapshot %d did not become ready", i+1)

				GinkgoWriter.Printf("[%s] Snapshot %d/%d created: %s\n", proto.name, i+1, numSnapshots, snapshotName)
			}

			By("Verifying all snapshots exist and are ready")
			for _, snapshotName := range snapshotNames {
				err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Snapshot %s should be ready", snapshotName)
			}

			By("Restoring from first and last snapshots to verify data integrity")
			// Restore from first snapshot
			restore1PVC := "snapshot-stress-restore-1-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restore1PVC, snapshotNames[0], proto.storageClass, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from first snapshot")
			// Register cleanup with PV wait (restored PVCs are clones)
			f.RegisterPVCCleanup(restore1PVC)

			// Restore from last snapshot
			restoreLastPVC := "snapshot-stress-restore-last-" + proto.id
			err = f.K8s.CreatePVCFromSnapshot(ctx, restoreLastPVC, snapshotNames[numSnapshots-1], proto.storageClass, "1Gi",
				[]corev1.PersistentVolumeAccessMode{proto.accessMode})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from last snapshot")
			// Register cleanup with PV wait (restored PVCs are clones)
			f.RegisterPVCCleanup(restoreLastPVC)

			By("Creating pods to verify restored data")
			restore1PodName := "snapshot-stress-restore-pod-1-" + proto.id
			restore1Pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restore1PodName,
				PVCName:   restore1PVC,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			restoreLastPodName := "snapshot-stress-restore-pod-last-" + proto.id
			restoreLastPod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      restoreLastPodName,
				PVCName:   restoreLastPVC,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, restore1Pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPodReady(ctx, restoreLastPod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred())

			err = f.K8s.WaitForPVCBound(ctx, restore1PVC, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restore PVC 1 did not become Bound")

			err = f.K8s.WaitForPVCBound(ctx, restoreLastPVC, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Restore PVC last did not become Bound")

			By("Verifying first snapshot has snapshot-1 data but not snapshot-5 data")
			data1, err := f.K8s.ExecInPod(ctx, restore1Pod.Name, []string{"cat", "/data/snapshot-1.txt"})
			Expect(err).NotTo(HaveOccurred())
			Expect(data1).To(ContainSubstring("Snapshot 1 Data"))

			exists, err := f.K8s.FileExistsInPod(ctx, restore1Pod.Name, fmt.Sprintf("/data/snapshot-%d.txt", numSnapshots))
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse(), "First snapshot should not have last snapshot's data")

			By("Verifying last snapshot has all data")
			dataLast, err := f.K8s.ExecInPod(ctx, restoreLastPod.Name, []string{"cat", fmt.Sprintf("/data/snapshot-%d.txt", numSnapshots)})
			Expect(err).NotTo(HaveOccurred())
			Expect(dataLast).To(ContainSubstring(fmt.Sprintf("Snapshot %d Data", numSnapshots)))

			GinkgoWriter.Printf("[%s] Successfully created and verified %d snapshots\n", proto.name, numSnapshots)
		})
	}
})

var _ = Describe("Volume Stress", func() {
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
		name         string // Display name for test output
		id           string // Lowercase identifier for K8s resource names (RFC 1123)
		storageClass string
		accessMode   corev1.PersistentVolumeAccessMode
		podTimeout   time.Duration
	}

	protocols := []protocolConfig{
		{
			name:         "NFS",
			id:           "nfs",
			storageClass: "tns-csi-nfs",
			accessMode:   corev1.ReadWriteMany,
			podTimeout:   2 * time.Minute,
		},
		{
			name:         "NVMe-oF",
			id:           "nvmeof",
			storageClass: "tns-csi-nvmeof",
			accessMode:   corev1.ReadWriteOnce,
			podTimeout:   6 * time.Minute,
		},
		{
			name:         "iSCSI",
			id:           "iscsi",
			storageClass: "tns-csi-iscsi",
			accessMode:   corev1.ReadWriteOnce,
			podTimeout:   6 * time.Minute,
		},
	}

	for _, proto := range protocols {
		It("should handle multiple volumes concurrently ["+proto.name+"]", func() {
			ctx := context.Background()
			numVolumes := 3

			By(fmt.Sprintf("Creating %d PVCs concurrently", numVolumes))
			pvcNames := make([]string, numVolumes)
			var wg sync.WaitGroup
			errChan := make(chan error, numVolumes)

			for i := range numVolumes {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					pvcName := fmt.Sprintf("stress-%s-pvc-%d", proto.id, index+1)
					pvcNames[index] = pvcName

					_, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
						Name:             pvcName,
						StorageClassName: proto.storageClass,
						Size:             "1Gi",
						AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
					})
					if err != nil {
						errChan <- fmt.Errorf("failed to create PVC %s: %w", pvcName, err)
						return
					}
					f.Cleanup.Add(func() error {
						return f.K8s.DeletePVC(context.Background(), pvcName)
					})
				}(i)
			}

			wg.Wait()
			close(errChan)

			for err := range errChan {
				Expect(err).NotTo(HaveOccurred())
			}

			// For NFS with Immediate binding, wait for PVCs to bind first
			// For NVMe-oF with WaitForFirstConsumer, PVCs bind when pod is created
			if proto.accessMode == corev1.ReadWriteMany {
				By("Waiting for all PVCs to become Bound (NFS)")
				for _, pvcName := range pvcNames {
					err := f.K8s.WaitForPVCBound(ctx, pvcName, 2*time.Minute)
					Expect(err).NotTo(HaveOccurred(), "PVC %s did not become Bound", pvcName)
					GinkgoWriter.Printf("[%s] PVC %s bound\n", proto.name, pvcName)
				}
			}

			By(fmt.Sprintf("Creating %d pods concurrently", numVolumes))
			podNames := make([]string, numVolumes)
			errChan = make(chan error, numVolumes)

			for i := range numVolumes {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					podName := fmt.Sprintf("stress-%s-pod-%d", proto.id, index+1)
					podNames[index] = podName

					_, err := f.K8s.CreatePod(ctx, framework.PodOptions{
						Name:      podName,
						PVCName:   pvcNames[index],
						MountPath: "/data",
						Command:   []string{"sh", "-c", fmt.Sprintf("echo 'Pod %d data' > /data/test.txt && sync && sleep 300", index+1)},
					})
					if err != nil {
						errChan <- fmt.Errorf("failed to create pod %s: %w", podName, err)
						return
					}
					f.Cleanup.Add(func() error {
						return f.K8s.DeletePod(context.Background(), podName)
					})
				}(i)
			}

			wg.Wait()
			close(errChan)

			for err := range errChan {
				Expect(err).NotTo(HaveOccurred())
			}

			By("Waiting for all pods to become Ready")
			for _, podName := range podNames {
				err := f.K8s.WaitForPodReady(ctx, podName, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred(), "Pod %s did not become Ready", podName)
				GinkgoWriter.Printf("[%s] Pod %s ready\n", proto.name, podName)
			}

			// For NVMe-oF, verify PVCs are bound after pods are ready
			if proto.accessMode == corev1.ReadWriteOnce {
				By("Verifying all PVCs are Bound (NVMe-oF)")
				for _, pvcName := range pvcNames {
					err := f.K8s.WaitForPVCBound(ctx, pvcName, 30*time.Second)
					Expect(err).NotTo(HaveOccurred(), "PVC %s should be Bound", pvcName)
				}
			}

			By("Verifying data in all volumes")
			for i, podName := range podNames {
				output, err := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/test.txt"})
				Expect(err).NotTo(HaveOccurred(), "Failed to read from pod %s", podName)
				Expect(output).To(ContainSubstring(fmt.Sprintf("Pod %d data", i+1)))
			}

			GinkgoWriter.Printf("[%s] Successfully created and verified %d concurrent volumes\n", proto.name, numVolumes)
		})
	}
})
