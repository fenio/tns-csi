// Package iscsi contains iSCSI-specific E2E tests for the TrueNAS CSI driver.
package iscsi

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("Name Templating", func() {
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

	It("should create volumes with templated names from StorageClass parameters", func() {
		ctx := context.Background()
		scName := "tns-csi-iscsi-name-template"
		podTimeout := 6 * time.Minute

		By("Creating StorageClass with nameTemplate parameter")
		params := map[string]string{
			"protocol":     "iscsi",
			"pool":         f.Config.TrueNASPool,
			"server":       f.Config.TrueNASHost,
			"nameTemplate": "{{ .PVCNamespace }}-{{ .PVCName }}",
			"fsType":       "ext4",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with nameTemplate")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with templated StorageClass")
		pvcName := "name-template-test-iscsi"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating test POD (required for WaitForFirstConsumer binding)")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "name-template-pod-iscsi",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Verifying volume handle contains templated name")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")

		expectedPattern := fmt.Sprintf("%s-%s", f.Namespace(), pvcName)
		Expect(volumeHandle).To(ContainSubstring(expectedPattern),
			"Volume handle should contain templated name: %s", expectedPattern)

		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
			GinkgoWriter.Printf("Expected pattern: %s\n", expectedPattern)
		}

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Verifying I/O works on templated volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'test data' > /data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write to volume")

		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read from volume")
		Expect(output).To(ContainSubstring("test data"))
	})

	It("should create volumes with prefix and suffix from StorageClass parameters", func() {
		ctx := context.Background()
		scName := "tns-csi-iscsi-prefix-suffix"

		By("Creating StorageClass with namePrefix and nameSuffix")
		params := map[string]string{
			"protocol":   "iscsi",
			"pool":       f.Config.TrueNASPool,
			"server":     f.Config.TrueNASHost,
			"namePrefix": "prod-",
			"nameSuffix": "-data",
			"fsType":     "ext4",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with prefix/suffix")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with prefix/suffix StorageClass")
		pvcName := "prefix-suffix-test-iscsi"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: scName,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating test POD (required for WaitForFirstConsumer binding)")
		_, err = f.CreatePod(ctx, framework.PodOptions{
			Name:      "prefix-suffix-pod-iscsi",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Verifying volume handle contains prefix and suffix")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")

		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle with prefix/suffix: %s\n", volumeHandle)
		}
		Expect(volumeHandle).To(ContainSubstring("prod-"), "Volume handle should contain prefix 'prod-'")
		Expect(volumeHandle).To(ContainSubstring("-data"), "Volume handle should contain suffix '-data'")
	})
})
