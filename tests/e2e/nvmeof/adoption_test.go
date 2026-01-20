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

var _ = Describe("NVMe-oF Volume Adoption", func() {
	var f *framework.Framework
	var ctx context.Context

	// Timeouts
	const (
		pvcTimeout    = 120 * time.Second
		podTimeout    = 120 * time.Second
		deleteTimeout = 60 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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

	It("should adopt an orphaned volume with markAdoptable=true when adoptExisting=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		adoptableStorageClass := "tns-csi-nvmeof-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, adoptableStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "nvmeof",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"transport":      "tcp",
			"port":           "4420",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptableStorageClass)
		})

		By("Creating the original PVC")
		pvcName := "test-pvc-nvmeof-adoption"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: adoptableStorageClass,
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

		zvolPath := fmt.Sprintf("%s/%s", f.Config.TrueNASPool, volumeHandle)
		subsystemNQN := "nqn.2137.csi.tns:" + volumeHandle
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected ZVOL path on TrueNAS: %s\n", zvolPath)
		GinkgoWriter.Printf("Expected NVMe-oF subsystem NQN: %s\n", subsystemNQN)

		By("Creating a pod to write test data")
		podName := "test-pod-nvmeof-adoption"
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
		testData := "NVMe-oF Adoption Test Data - Do Not Lose This!"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/adoption-test.txt && sync", testData),
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

		By("Deleting the NVMe-oF subsystem to simulate orphaned volume (ZVOL exists, but subsystem is missing)")
		err = f.TrueNAS.DeleteNVMeOFSubsystem(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying NVMe-oF subsystem was deleted")
		subsystemExists, err := f.TrueNAS.NVMeOFSubsystemExists(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystemExists).To(BeFalse(), "NVMe-oF subsystem should be deleted")
		GinkgoWriter.Printf("Volume is now orphaned: ZVOL exists at %s but NVMe-oF subsystem is missing\n", zvolPath)

		By("Creating StorageClass with adoptExisting=true for adoption")
		adoptingStorageClass := "tns-csi-nvmeof-adopting"
		err = f.K8s.CreateStorageClassWithParams(ctx, adoptingStorageClass, "tns.csi.io", map[string]string{
			"protocol":      "nvmeof",
			"server":        f.Config.TrueNASHost,
			"pool":          f.Config.TrueNASPool,
			"transport":     "tcp",
			"port":          "4420",
			"fsType":        "ext4",
			"adoptExisting": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptingStorageClass)
		})

		By("Creating a new PVC with the adopting StorageClass")
		adoptedPVCName := "test-pvc-nvmeof-adoption-new"
		adoptedPVC, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             adoptedPVCName,
			StorageClassName: adoptingStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptedPVC).NotTo(BeNil())
		f.RegisterPVCCleanup(adoptedPVCName)

		By("Waiting for adopted PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, adoptedPVCName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting the new PV name and volume handle")
		newPVName, err := f.K8s.GetPVName(ctx, adoptedPVCName)
		Expect(err).NotTo(HaveOccurred())
		newVolumeHandle, err := f.K8s.GetVolumeHandle(ctx, newPVName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("New volume handle: %s\n", newVolumeHandle)

		By("Creating a pod to verify the new volume")
		adoptedPodName := "test-pod-nvmeof-adopted"
		adoptedPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      adoptedPodName,
			PVCName:   adoptedPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptedPod).NotTo(BeNil())

		By("Waiting for adopted pod to be ready")
		err = f.K8s.WaitForPodReady(ctx, adoptedPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the volume is usable")
		_, err = f.K8s.ExecInPod(ctx, adoptedPodName, []string{
			"sh", "-c", "echo 'test' > /data/new-test.txt && cat /data/new-test.txt",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Cleaning up adopted resources")
		err = f.K8s.DeletePod(ctx, adoptedPodName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, adoptedPodName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Cleaning up the orphaned ZVOL from TrueNAS")
		// Delete the subsystem first if it was recreated
		newSubsystemNQN := "nqn.2137.csi.tns:" + newVolumeHandle
		_ = f.TrueNAS.DeleteNVMeOFSubsystem(ctx, newSubsystemNQN)
		_ = f.TrueNAS.DeleteNVMeOFSubsystem(ctx, subsystemNQN)
		err = f.TrueNAS.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Cleaned up orphaned ZVOL: %s\n", zvolPath)
	})

	It("should mark a volume as adoptable when markAdoptable=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		markAdoptableStorageClass := "tns-csi-nvmeof-mark-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, markAdoptableStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "nvmeof",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"transport":      "tcp",
			"port":           "4420",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, markAdoptableStorageClass)
		})

		By("Creating a PVC")
		pvcName := "test-pvc-nvmeof-marked-adoptable"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: markAdoptableStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting volume handle")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())

		zvolPath := fmt.Sprintf("%s/%s", f.Config.TrueNASPool, volumeHandle)
		subsystemNQN := "nqn.2137.csi.tns:" + volumeHandle
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("ZVOL path: %s\n", zvolPath)

		By("Verifying adoptable property is set on TrueNAS dataset")
		Expect(f.TrueNAS).NotTo(BeNil())
		adoptableValue, err := f.TrueNAS.GetDatasetProperty(ctx, zvolPath, "tns-csi:adoptable")
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptableValue).To(Equal("true"), "Dataset should have tns-csi:adoptable=true")
		GinkgoWriter.Printf("Dataset %s has adoptable property set to: %s\n", zvolPath, adoptableValue)

		By("Deleting PVC to trigger retain (not delete)")
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying dataset still exists on TrueNAS after deletion")
		exists, err := f.TrueNAS.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Dataset should be retained with deleteStrategy=retain")

		By("Cleaning up retained resources from TrueNAS")
		_ = f.TrueNAS.DeleteNVMeOFSubsystem(ctx, subsystemNQN)
		err = f.TrueNAS.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Cleaned up retained dataset: %s\n", zvolPath)
	})
})
