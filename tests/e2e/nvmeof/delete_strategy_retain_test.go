package nvmeof_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Delete Strategy Retain", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts
	const (
		pvcTimeout    = 120 * time.Second
		podTimeout    = 120 * time.Second
		deleteTimeout = 60 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())

		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should retain TrueNAS resources when deleteStrategy=retain is set", func() {
		By("Creating StorageClass with deleteStrategy=retain")
		retainStorageClass := "tns-csi-nvmeof-retain"
		err = f.K8s.CreateStorageClassWithParams(ctx, retainStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "nvmeof",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"transport":      "tcp",
			"port":           "4420",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, retainStorageClass)
		})

		By("Creating a PVC with retain StorageClass")
		pvcName := "test-pvc-retain"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: retainStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting PV name and volume handle for later verification")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvName).NotTo(BeEmpty())

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(volumeHandle).NotTo(BeEmpty())

		// Volume handle is just the volume name (e.g., pvc-xxx)
		// The ZVOL path on TrueNAS will be: pool/parentDataset/volumeName
		// Since we use the default parentDataset (same as pool), the path is: pool/volumeName
		zvolPath := fmt.Sprintf("%s/%s", f.Config.TrueNASPool, volumeHandle)
		// NVMe-oF subsystem NQN format: nqn.2137.csi.tns:<volume-name>
		subsystemNQN := "nqn.2137.csi.tns:" + volumeHandle
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected ZVOL path on TrueNAS: %s\n", zvolPath)
		GinkgoWriter.Printf("Expected NVMe-oF subsystem NQN: %s\n", subsystemNQN)

		By("Creating a pod to verify volume works")
		podName := "test-pod-retain"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test data to verify volume is working")
		testData := "Retain Test Data NVMe-oF"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/retain-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the pod")
		err = f.K8s.DeletePod(ctx, podName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, podName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the PVC (triggers DeleteVolume with retain strategy)")
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PV to be deleted from Kubernetes")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying ZVOL still exists on TrueNAS")
		Expect(f.TrueNAS).NotTo(BeNil(), "TrueNAS verifier must be available for this test")
		exists, err := f.TrueNAS.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "ZVOL should still exist on TrueNAS after PVC deletion with deleteStrategy=retain")

		By("Verifying NVMe-oF subsystem still exists on TrueNAS")
		subsystemExists, err := f.TrueNAS.NVMeOFSubsystemExists(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystemExists).To(BeTrue(), "NVMe-oF subsystem should still exist on TrueNAS after PVC deletion with deleteStrategy=retain")

		By("ZVOL and subsystem confirmed to still exist on TrueNAS - retain strategy working correctly")
		GinkgoWriter.Printf("Successfully verified ZVOL %s was retained on TrueNAS\n", zvolPath)
		GinkgoWriter.Printf("Successfully verified NVMe-oF subsystem %s was retained on TrueNAS\n", subsystemNQN)

		By("Cleaning up retained NVMe-oF subsystem from TrueNAS")
		err = f.TrueNAS.DeleteNVMeOFSubsystem(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained NVMe-oF subsystem from TrueNAS")

		By("Cleaning up retained ZVOL from TrueNAS")
		err = f.TrueNAS.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained ZVOL from TrueNAS")

		By("Verifying ZVOL was successfully deleted from TrueNAS")
		exists, err = f.TrueNAS.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "ZVOL should no longer exist on TrueNAS after cleanup")

		By("Verifying NVMe-oF subsystem was successfully deleted from TrueNAS")
		subsystemExists, err = f.TrueNAS.NVMeOFSubsystemExists(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystemExists).To(BeFalse(), "NVMe-oF subsystem should no longer exist on TrueNAS after cleanup")

		By("Cleanup verified - ZVOL and subsystem successfully removed from TrueNAS")
		GinkgoWriter.Printf("Successfully cleaned up ZVOL %s from TrueNAS\n", zvolPath)
		GinkgoWriter.Printf("Successfully cleaned up NVMe-oF subsystem %s from TrueNAS\n", subsystemNQN)
	})
})
