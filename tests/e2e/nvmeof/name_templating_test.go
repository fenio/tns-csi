// Package nvmeof contains NVMe-oF-specific E2E tests for the TrueNAS CSI driver.
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

var _ = Describe("Name Templating", func() {
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

	It("should create volumes with templated names from StorageClass parameters", func() {
		ctx := context.Background()
		scName := "tns-csi-nvmeof-name-template"
		podTimeout := 2 * time.Minute

		By("Creating StorageClass with nameTemplate parameter")
		params := map[string]string{
			"protocol":     "nvmeof",
			"pool":         f.Config.TrueNASPool,
			"server":       f.Config.TrueNASHost,
			"fsType":       "ext4",
			"nameTemplate": "{{ .PVCNamespace }}-{{ .PVCName }}",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with nameTemplate")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with templated StorageClass")
		pvcName := "name-template-test-nvmeof"
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

		By("Creating test POD to verify volume works")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "name-template-pod-nvmeof",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

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
		scName := "tns-csi-nvmeof-prefix-suffix"

		By("Creating StorageClass with namePrefix and nameSuffix")
		params := map[string]string{
			"protocol":   "nvmeof",
			"pool":       f.Config.TrueNASPool,
			"server":     f.Config.TrueNASHost,
			"fsType":     "ext4",
			"namePrefix": "prod-",
			"nameSuffix": "-data",
		}
		err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
		Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with prefix/suffix")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(context.Background(), scName)
		})

		By("Creating PVC with prefix/suffix StorageClass")
		pvcName := "prefix-suffix-test-nvmeof"
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
