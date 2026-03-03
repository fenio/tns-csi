// Package smb contains E2E tests for SMB protocol support.
package smb

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("SMB Volume Adoption", func() {
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

		err = f.Setup("smb")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should adopt an orphaned volume with markAdoptable=true when adoptExisting=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		adoptableStorageClass := "tns-csi-smb-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, adoptableStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "smb",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptableStorageClass)
		})

		By("Creating the original PVC")
		pvcName := "test-pvc-adoption"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: adoptableStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
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

		datasetPath := volumeHandle
		smbSharePath := "/mnt/" + volumeHandle
		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		}
		if f.Verbose() {
			GinkgoWriter.Printf("Expected dataset path on TrueNAS: %s\n", datasetPath)
		}
		if f.Verbose() {
			GinkgoWriter.Printf("Expected SMB share path on TrueNAS: %s\n", smbSharePath)
		}

		By("Creating a POD to write test data")
		podName := "test-pod-adoption"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test data to verify volume is working")
		testData := "Adoption Test Data - Do Not Lose This!"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/adoption-test.txt", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the POD")
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

		By("Verifying dataset still exists on TrueNAS")
		Expect(f.TrueNAS).NotTo(BeNil(), "TrueNAS verifier must be available for this test")
		exists, err := f.TrueNAS.DatasetExists(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Dataset should still exist on TrueNAS after PVC deletion with deleteStrategy=retain")

		By("Deleting the SMB share to simulate orphaned volume (dataset exists, but share is missing)")
		err = f.TrueNAS.DeleteSMBShare(ctx, smbSharePath)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying SMB share was deleted")
		shareExists, err := f.TrueNAS.SMBShareExists(ctx, smbSharePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(shareExists).To(BeFalse(), "SMB share should be deleted")
		if f.Verbose() {
			GinkgoWriter.Printf("Volume is now orphaned: dataset exists at %s but SMB share is missing\n", datasetPath)
		}

		By("Creating StorageClass with adoptExisting=true for adoption")
		adoptingStorageClass := "tns-csi-smb-adopting"
		err = f.K8s.CreateStorageClassWithParams(ctx, adoptingStorageClass, "tns.csi.io", map[string]string{
			"protocol":      "smb",
			"server":        f.Config.TrueNASHost,
			"pool":          f.Config.TrueNASPool,
			"adoptExisting": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptingStorageClass)
		})

		By("Creating a new PVC with the same name to trigger adoption")
		adoptedPVCName := "test-pvc-adoption-new"

		adoptedPVC, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             adoptedPVCName,
			StorageClassName: adoptingStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
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
		if f.Verbose() {
			GinkgoWriter.Printf("New volume handle: %s\n", newVolumeHandle)
		}

		By("Creating a POD to verify the new volume")
		adoptedPodName := "test-pod-adopted"
		adoptedPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      adoptedPodName,
			PVCName:   adoptedPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptedPod).NotTo(BeNil())

		By("Waiting for adopted POD to be ready")
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

		By("Cleaning up the orphaned dataset from TrueNAS")
		// The original dataset is still there, clean it up
		err = f.TrueNAS.DeleteDataset(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		if f.Verbose() {
			GinkgoWriter.Printf("Cleaned up orphaned dataset: %s\n", datasetPath)
		}
	})

	It("should not adopt a volume when adoptExisting=false (default)", func() {
		By("Creating StorageClass with markAdoptable=true but without adoptExisting")
		nonAdoptingStorageClass := "tns-csi-smb-nonadopt"
		err := f.K8s.CreateStorageClassWithParams(ctx, nonAdoptingStorageClass, "tns.csi.io", map[string]string{
			"protocol":      "smb",
			"server":        f.Config.TrueNASHost,
			"pool":          f.Config.TrueNASPool,
			"markAdoptable": "true",
			// adoptExisting defaults to false
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, nonAdoptingStorageClass)
		})

		By("Creating a PVC")
		pvcName := "test-pvc-noadopt"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: nonAdoptingStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying volume was created (not adopted)")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvName).NotTo(BeEmpty())

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(volumeHandle).NotTo(BeEmpty())
		if f.Verbose() {
			GinkgoWriter.Printf("Created new volume (no adoption): %s\n", volumeHandle)
		}

		By("Verifying dataset exists on TrueNAS")
		Expect(f.TrueNAS).NotTo(BeNil())
		datasetPath := volumeHandle
		exists, err := f.TrueNAS.DatasetExists(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
	})

	It("should mark a volume as adoptable when markAdoptable=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		markAdoptableStorageClass := "tns-csi-smb-mark-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, markAdoptableStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "smb",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, markAdoptableStorageClass)
		})

		By("Creating a PVC")
		pvcName := "test-pvc-marked-adoptable"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: markAdoptableStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
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

		datasetPath := volumeHandle
		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		}
		if f.Verbose() {
			GinkgoWriter.Printf("Dataset path: %s\n", datasetPath)
		}

		By("Verifying adoptable property is set on TrueNAS dataset")
		Expect(f.TrueNAS).NotTo(BeNil())
		adoptableValue, err := f.TrueNAS.GetDatasetProperty(ctx, datasetPath, "tns-csi:adoptable")
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptableValue).To(Equal("true"), "Dataset should have tns-csi:adoptable=true")
		if f.Verbose() {
			GinkgoWriter.Printf("Dataset %s has adoptable property set to: %s\n", datasetPath, adoptableValue)
		}

		By("Deleting PVC to trigger retain (not delete)")
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying dataset still exists on TrueNAS after deletion")
		exists, err := f.TrueNAS.DatasetExists(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Dataset should be retained with deleteStrategy=retain")

		By("Cleaning up retained resources from TrueNAS")
		smbSharePath := "/mnt/" + volumeHandle
		err = f.TrueNAS.DeleteSMBShare(ctx, smbSharePath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained SMB share from TrueNAS")
		err = f.TrueNAS.DeleteDataset(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		if f.Verbose() {
			GinkgoWriter.Printf("Cleaned up retained dataset: %s\n", datasetPath)
		}
	})
})
