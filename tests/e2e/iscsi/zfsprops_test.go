package iscsi_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI ZFS Properties", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	const (
		pvcTimeout    = 360 * time.Second
		podTimeout    = 360 * time.Second
		deleteTimeout = 180 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())

		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create ZVOL with custom ZFS properties and delete it cleanly", func() {
		By("Creating StorageClass with ZFS properties for iSCSI")
		zfsStorageClass := "tns-csi-iscsi-zfsprops"
		err = f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, zfsStorageClass, "tns.csi.io", map[string]string{
			"protocol":         "iscsi",
			"server":           f.Config.TrueNASHost,
			"pool":             f.Config.TrueNASPool,
			"port":             "3260",
			"fsType":           "ext4",
			"zfs.compression":  "lz4",
			"zfs.volblocksize": "16K",
		}, "Immediate")
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, zfsStorageClass)
		})

		By("Creating PVC")
		pvcName := "test-pvc-iscsi-zfsprops"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: zfsStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating POD to trigger provisioning")
		podName := "test-pod-iscsi-zfsprops"
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

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting dataset path from PV")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		datasetPath, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(datasetPath).NotTo(BeEmpty())

		By("Verifying compression is set to lz4")
		compression, err := f.TrueNAS.GetZFSProperty(ctx, datasetPath, "compression")
		Expect(err).NotTo(HaveOccurred())
		Expect(compression).To(Equal("LZ4"), "compression should be LZ4")

		By("Verifying volblocksize is set to 16K")
		volblocksize, err := f.TrueNAS.GetZFSProperty(ctx, datasetPath, "volblocksize")
		Expect(err).NotTo(HaveOccurred())
		Expect(volblocksize).To(Equal("16K"), "volblocksize should be 16K")

		By("Deleting the pod and PVC")
		err = f.K8s.DeletePod(ctx, podName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, podName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the ZVOL was deleted from TrueNAS")
		Expect(f.TrueNAS).NotTo(BeNil(), "TrueNAS verifier must be available for cleanup verification")
		err = f.VerifyTrueNASCleanup(ctx, datasetPath, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
	})
})
