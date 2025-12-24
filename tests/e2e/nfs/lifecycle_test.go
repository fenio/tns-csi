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

var _ = Describe("NFS PVC Lifecycle", func() {
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

	It("should create and delete PVC without pod attachment (controller-only path)", func() {
		ctx := context.Background()

		By("Creating PVC for lifecycle test")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "lifecycle-pvc-nfs",
			StorageClassName: "tns-csi-nfs",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

		By("Waiting for PVC to become Bound (NFS binds immediately)")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Getting PV name")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC")
		pvName := pvc.Spec.VolumeName
		Expect(pvName).NotTo(BeEmpty(), "PVC should have a bound PV")
		GinkgoWriter.Printf("Created PV: %s\n", pvName)

		By("Getting PV details")
		pv, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV")
		Expect(pv.Spec.CSI).NotTo(BeNil(), "PV should be CSI volume")
		GinkgoWriter.Printf("Volume handle (TrueNAS dataset): %s\n", pv.Spec.CSI.VolumeHandle)

		By("Deleting PVC directly (without pod attachment)")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC")

		By("Waiting for PVC to be deleted")
		err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 90*time.Second)
		Expect(err).NotTo(HaveOccurred(), "PVC was not deleted in time")

		By("Waiting for PV to be deleted (indicates TrueNAS cleanup)")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, 90*time.Second)
		Expect(err).NotTo(HaveOccurred(), "PV was not deleted in time - backend cleanup may have failed")
	})

	It("should handle multiple PVC create/delete cycles", func() {
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			By(fmt.Sprintf("Creating PVC (cycle %d)", i))
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "lifecycle-cycle-pvc-nfs",
				StorageClassName: "tns-csi-nfs",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC in cycle %d", i)

			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound in cycle %d", i)

			pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			pvName := pvc.Spec.VolumeName
			GinkgoWriter.Printf("Cycle %d: Created PV %s\n", i, pvName)

			By(fmt.Sprintf("Deleting PVC (cycle %d)", i))
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC in cycle %d", i)

			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 90*time.Second)
			Expect(err).NotTo(HaveOccurred(), "PVC was not deleted in cycle %d", i)

			err = f.K8s.WaitForPVDeleted(ctx, pvName, 90*time.Second)
			Expect(err).NotTo(HaveOccurred(), "PV was not deleted in cycle %d", i)
		}
	})
})
