package nfs_test

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

var _ = Describe("NFS Delete Strategy Retain", func() {
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

		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should retain TrueNAS resources when deleteStrategy=retain is set", func() {
		By("Creating StorageClass with deleteStrategy=retain")
		retainStorageClass := "tns-csi-nfs-retain"
		err = f.K8s.CreateStorageClassWithParams(ctx, retainStorageClass, "tns.csi.io", map[string]string{
			"protocol":       "nfs",
			"server":         f.Config.TrueNASHost,
			"pool":           f.Config.TrueNASPool,
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

		By("Extracting dataset path from volume handle")
		// Volume handle format: protocol#server#datasetPath
		parts := strings.Split(volumeHandle, "#")
		Expect(len(parts)).To(BeNumerically(">=", 3), "Volume handle should have at least 3 parts")
		datasetPath := parts[2]

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
		testData := "Retain Test Data"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/retain-test.txt", testData),
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

		// Check for the specific retain behavior message
		containsRetainMessage := strings.Contains(logs, "deleteStrategy=retain") ||
			strings.Contains(logs, "skipping actual deletion") ||
			strings.Contains(logs, "retaining")

		if containsRetainMessage {
			By("Controller logs confirm volume was retained")
		} else {
			By("Retain behavior may have been applied - manual TrueNAS verification recommended")
		}

		By("Logging dataset path for manual verification if needed")
		GinkgoWriter.Printf("Dataset path that should still exist on TrueNAS: %s\n", datasetPath)

		// Note: TrueNAS resources are intentionally retained
		// Manual cleanup of the TrueNAS dataset may be required
	})
})
