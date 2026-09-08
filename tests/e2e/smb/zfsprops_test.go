// Package smb contains E2E tests for SMB protocol support.
package smb

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("SMB ZFS Properties", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	const (
		pvcTimeout = 120 * time.Second
		podTimeout = 120 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
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

	It("should create volume with custom ZFS properties", func() {
		By("Creating StorageClass with ZFS properties")
		zfsStorageClass := "tns-csi-smb-zfsprops"
		err = f.K8s.CreateStorageClassWithParams(ctx, zfsStorageClass, "tns.csi.io", map[string]string{
			"protocol":        "smb",
			"server":          f.Config.TrueNASHost,
			"pool":            f.Config.TrueNASPool,
			"zfs.compression": "lz4",
			"zfs.atime":       "off",
			"zfs.recordsize":  "128K",
			"csi.storage.k8s.io/node-stage-secret-name":      "tns-csi-smb-creds",
			"csi.storage.k8s.io/node-stage-secret-namespace": "tns-csi",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, zfsStorageClass)
		})

		By("Creating PVC")
		pvcName := "test-pvc-zfsprops"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: zfsStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating POD to trigger provisioning")
		podName := "test-pod-zfsprops"
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

		By("Verifying atime is set to off")
		atime, err := f.TrueNAS.GetZFSProperty(ctx, datasetPath, "atime")
		Expect(err).NotTo(HaveOccurred())
		Expect(atime).To(Equal("OFF"), "atime should be OFF")

		By("Verifying recordsize is set to 128K")
		recordsize, err := f.TrueNAS.GetZFSProperty(ctx, datasetPath, "recordsize")
		Expect(err).NotTo(HaveOccurred())
		Expect(recordsize).To(Equal("128K"), "recordsize should be 128K")
	})
})
