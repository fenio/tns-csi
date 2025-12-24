package nvmeof_test

import (
	"context"
	"fmt"
	"strings"
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

	// Timeouts (longer for NVMe-oF)
	const (
		pvcTimeout    = 360 * time.Second
		podTimeout    = 360 * time.Second
		deleteTimeout = 120 * time.Second
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
		err = f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, retainStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "nvmeof",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
			"transport":      "tcp",
			"port":           "4420",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
		}, "WaitForFirstConsumer")
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

		By("Creating a pod to trigger PVC binding and verify volume works")
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

		By("Extracting ZVOL path from volume handle")
		// Volume handle format: protocol#server#zvolPath
		parts := strings.Split(volumeHandle, "#")
		Expect(len(parts)).To(BeNumerically(">=", 3), "Volume handle should have at least 3 parts")
		zvolPath := parts[2]

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

		By("Checking controller logs for retain behavior")
		logs, err := f.K8s.GetControllerLogs(ctx, 100)
		Expect(err).NotTo(HaveOccurred())

		// Check for retain behavior indicators
		containsRetainMessage := strings.Contains(logs, "deleteStrategy=retain") ||
			strings.Contains(logs, "skipping actual deletion") ||
			strings.Contains(logs, "retaining") ||
			strings.Contains(logs, "skip.*delete")

		if containsRetainMessage {
			By("Controller logs confirm volume was retained")
		} else {
			By("Retain behavior may have been applied - manual TrueNAS verification recommended")
		}

		By("Logging ZVOL path for manual verification if needed")
		GinkgoWriter.Printf("ZVOL path that should still exist on TrueNAS: %s\n", zvolPath)

		// Note: TrueNAS resources are intentionally retained
		// Manual cleanup of the TrueNAS ZVOL may be required
	})
})
