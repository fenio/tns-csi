// Package iscsi contains E2E tests for iSCSI protocol support.
package iscsi

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Volume Clone", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create a clone from an existing volume and verify data independence", func() {
		ctx := context.Background()

		By("Creating source PVC")
		sourcePVC, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "clone-source-pvc-iscsi",
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, sourcePVC.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Creating source pod")
		sourcePod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "clone-source-pod-iscsi",
			PVCName:   sourcePVC.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source pod")

		By("Waiting for source pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, sourcePod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source pod did not become ready")

		By("Writing test data to source volume")
		_, err = f.K8s.ExecInPod(ctx, sourcePod.Name, []string{"sh", "-c", "echo 'Source Volume Data' > /data/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Verifying test data on source volume")
		output, err := f.K8s.ExecInPod(ctx, sourcePod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data from source")
		Expect(output).To(Equal("Source Volume Data"))

		By("Creating clone PVC from source PVC")
		clonePVCName := "clone-pvc-iscsi"
		err = f.K8s.CreatePVCFromPVC(ctx, clonePVCName, sourcePVC.Name, "tns-csi-iscsi", "1Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
		Expect(err).NotTo(HaveOccurred(), "Failed to create clone PVC")
		// Register cleanup with PV wait (clone must be fully deleted before source)
		f.RegisterPVCCleanup(clonePVCName)

		By("Waiting for clone PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, clonePVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Clone PVC did not become Bound")

		By("Creating pod to mount cloned volume")
		clonePod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "clone-pod-iscsi",
			PVCName:   clonePVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create clone pod")

		By("Waiting for clone pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, clonePod.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Clone pod did not become ready")

		By("Listing cloned volume contents")
		output, err = f.K8s.ExecInPod(ctx, clonePod.Name, []string{"ls", "-la", "/data/"})
		Expect(err).NotTo(HaveOccurred(), "Failed to list clone contents")
		GinkgoWriter.Printf("Clone volume contents:\n%s\n", output)

		By("Verifying cloned data is present")
		output, err = f.K8s.ExecInPod(ctx, clonePod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data from clone")
		Expect(output).To(Equal("Source Volume Data"), "Data mismatch in clone")

		By("Writing new data to cloned volume (testing independence)")
		_, err = f.K8s.ExecInPod(ctx, clonePod.Name, []string{"sh", "-c", "echo 'Data written to clone' > /data/clone-data.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to clone")

		By("Verifying new data on cloned volume")
		output, err = f.K8s.ExecInPod(ctx, clonePod.Name, []string{"cat", "/data/clone-data.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read new data from clone")
		Expect(output).To(Equal("Data written to clone"))

		By("Verifying source volume is unaffected (clone is independent)")
		exists, err := f.K8s.FileExistsInPod(ctx, sourcePod.Name, "/data/clone-data.txt")
		Expect(err).NotTo(HaveOccurred(), "Failed to check file existence on source")
		Expect(exists).To(BeFalse(), "Clone data should NOT appear in source volume - volumes are not independent!")
	})
})
