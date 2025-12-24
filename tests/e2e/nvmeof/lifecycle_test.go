// Package nvmeof contains E2E tests for NVMe-oF protocol support.
package nvmeof

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF PVC Lifecycle", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create and delete PVC after pod detachment (controller deletion path)", func() {
		ctx := context.Background()

		By("Creating PVC for lifecycle test")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "lifecycle-pvc-nvmeof",
			StorageClassName: "tns-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")

		By("Creating temporary pod to trigger PVC binding (WaitForFirstConsumer)")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "lifecycle-pod-nvmeof",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create pod")

		By("Waiting for pod to be ready (triggers PVC binding)")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying PVC is now bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 30*time.Second)
		Expect(err).NotTo(HaveOccurred(), "PVC should be bound after pod creation")

		By("Getting PV details")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC")
		pvName := pvc.Spec.VolumeName
		Expect(pvName).NotTo(BeEmpty(), "PVC should have a bound PV")
		GinkgoWriter.Printf("Created PV: %s\n", pvName)

		pv, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV")
		GinkgoWriter.Printf("Volume handle (TrueNAS zvol): %s\n", pv.Spec.CSI.VolumeHandle)

		By("Deleting pod (leaving PVC intact)")
		err = f.K8s.DeletePod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")

		err = f.K8s.WaitForPodDeleted(ctx, pod.Name, time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod was not deleted")

		By("Verifying PVC remains bound after pod deletion")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PVC")
		Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound), "PVC should remain bound after pod deletion")

		By("Deleting PVC directly (testing controller deletion path)")
		err = f.K8s.DeletePVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC")

		By("Waiting for PVC to be deleted")
		err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 90*time.Second)
		Expect(err).NotTo(HaveOccurred(), "PVC was not deleted in time")

		By("Waiting for PV to be deleted (indicates TrueNAS cleanup)")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, 120*time.Second)
		Expect(err).NotTo(HaveOccurred(), "PV was not deleted in time - backend cleanup may have failed")
	})

	It("should handle multiple PVC create/delete cycles", func() {
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			By(fmt.Sprintf("Creating PVC (cycle %d)", i))
			pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
				Name:             "lifecycle-cycle-pvc-nvmeof",
				StorageClassName: "tns-csi-nvmeof",
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC in cycle %d", i)

			By(fmt.Sprintf("Creating pod to trigger binding (cycle %d)", i))
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      "lifecycle-cycle-pod-nvmeof",
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create pod in cycle %d", i)

			err = f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready in cycle %d", i)

			pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			pvName := pvc.Spec.VolumeName
			GinkgoWriter.Printf("Cycle %d: Created PV %s\n", i, pvName)

			By(fmt.Sprintf("Deleting pod (cycle %d)", i))
			err = f.K8s.DeletePod(ctx, pod.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pod in cycle %d", i)

			err = f.K8s.WaitForPodDeleted(ctx, pod.Name, time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Pod was not deleted in cycle %d", i)

			By(fmt.Sprintf("Deleting PVC (cycle %d)", i))
			err = f.K8s.DeletePVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete PVC in cycle %d", i)

			err = f.K8s.WaitForPVCDeleted(ctx, pvc.Name, 90*time.Second)
			Expect(err).NotTo(HaveOccurred(), "PVC was not deleted in cycle %d", i)

			err = f.K8s.WaitForPVDeleted(ctx, pvName, 120*time.Second)
			Expect(err).NotTo(HaveOccurred(), "PV was not deleted in cycle %d", i)
		}
	})
})
