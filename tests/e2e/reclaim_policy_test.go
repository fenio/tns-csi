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

// These tests verify that reclaim policies (Delete/Retain) work correctly.
// This is tested across all protocols to ensure consistent behavior.

var _ = Describe("Reclaim Policy", func() {
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
		name         string
		id           string
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
		It("should delete PV when PVC with Delete policy is removed ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating StorageClass with Delete reclaim policy")
			scName := "tns-csi-" + proto.id + "-delete-policy"
			params := map[string]string{
				"protocol": proto.id,
				"pool":     f.Config.TrueNASPool,
				"server":   f.Config.TrueNASHost,
			}
			if proto.id != "nfs" {
				params["fsType"] = "ext4"
			}
			err := f.K8s.CreateStorageClassWithReclaimPolicy(ctx, scName, "tns.csi.io", params, corev1.PersistentVolumeReclaimDelete)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(context.Background(), scName)
			})

			By("Creating PVC")
			pvcName := "reclaim-delete-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

			By("Creating POD to trigger volume provisioning")
			podName := "reclaim-delete-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

			By("Getting PV name before deletion")
			pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")
			Expect(pvName).NotTo(BeEmpty(), "PV name should not be empty")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] PV created: %s\n", proto.name, pvName)
			}

			By("Deleting POD")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Pod was not deleted")

			By("Deleting PVC")
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC")
			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC was not deleted")

			By("Verifying PV is deleted (Delete policy)")
			err = f.K8s.WaitForPVDeleted(ctx, pvName, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PV should be deleted with Delete reclaim policy")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] Delete reclaim policy verified - PV was deleted\n", proto.name)
			}
		})

		It("should retain PV when PVC with Retain policy is removed ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating StorageClass with Retain reclaim policy")
			scName := "tns-csi-" + proto.id + "-retain-policy"
			params := map[string]string{
				"protocol": proto.id,
				"pool":     f.Config.TrueNASPool,
				"server":   f.Config.TrueNASHost,
			}
			if proto.id != "nfs" {
				params["fsType"] = "ext4"
			}
			err := f.K8s.CreateStorageClassWithReclaimPolicy(ctx, scName, "tns.csi.io", params, corev1.PersistentVolumeReclaimRetain)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(context.Background(), scName)
			})

			By("Creating PVC")
			pvcName := "reclaim-retain-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

			By("Creating POD to trigger volume provisioning")
			podName := "reclaim-retain-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

			By("Getting PV name before deletion")
			pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")
			Expect(pvName).NotTo(BeEmpty(), "PV name should not be empty")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] PV created: %s\n", proto.name, pvName)
			}

			By("Deleting POD")
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")
			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Pod was not deleted")

			By("Deleting PVC")
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC")
			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC was not deleted")

			By("Waiting briefly to ensure PV retention")
			time.Sleep(10 * time.Second)

			By("Verifying PV still exists (Retain policy)")
			pv, err := f.K8s.GetPV(ctx, pvName)
			Expect(err).NotTo(HaveOccurred(), "PV should still exist with Retain policy")
			Expect(pv).NotTo(BeNil(), "PV should not be nil")
			Expect(pv.Status.Phase).To(Equal(corev1.VolumeReleased), "PV should be in Released state")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] Retain reclaim policy verified - PV retained in %s state\n", proto.name, pv.Status.Phase)
			}

			By("Cleaning up retained PV")
			err = f.K8s.DeletePV(ctx, pvName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete retained PV")
		})
	}
})
