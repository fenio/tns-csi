// Package iscsi contains E2E tests for iSCSI protocol support.
package iscsi

import (
	"context"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Volume Expansion", func() {
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

	It("should expand volume size", func() {
		ctx := context.Background()

		By("Creating initial PVC with 1Gi size")
		pvcName := "expand-iscsi"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating POD to trigger volume provisioning and mount")
		podName := "expand-pod-iscsi"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Writing test data before expansion")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'pre-expansion data' > /data/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Verifying initial capacity")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		initialCapacity := pvc.Status.Capacity[corev1.ResourceStorage]

		By("Expanding PVC to 2Gi")
		err = f.K8s.ExpandPVC(ctx, pvc.Name, "2Gi")
		Expect(err).NotTo(HaveOccurred(), "Failed to request PVC expansion")

		By("Waiting for expansion to complete")
		err = f.K8s.WaitForPVCCapacity(ctx, pvc.Name, "2Gi", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC expansion did not complete")

		By("Verifying expanded capacity")
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		expandedCapacity := pvc.Status.Capacity[corev1.ResourceStorage]
		Expect(expandedCapacity.Cmp(initialCapacity)).To(BeNumerically(">", 0),
			"Expanded capacity should be greater than initial capacity")

		// PVC capacity only says the zvol grew. The pod must see a bigger filesystem too, which
		// needs the node to rescan the disk while it stays mounted (#328).
		By("Verifying the mounted filesystem grew inside the pod")
		Eventually(func() (int64, error) {
			out, execErr := f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "df -k /data | awk 'NR==2 {print $2}'"})
			if execErr != nil {
				return 0, execErr
			}
			return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		}, 2*time.Minute, 5*time.Second).Should(BeNumerically(">", 1536*1024),
			"filesystem should be larger than 1.5 GiB after expanding to 2Gi")

		By("Verifying data persisted after expansion")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read test data")
		Expect(output).To(ContainSubstring("pre-expansion data"), "Data should persist after expansion")

		By("Writing additional data to expanded volume")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'post-expansion data' >> /data/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Failed to write additional data")

		By("Verifying all data is accessible")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("pre-expansion data"))
		Expect(output).To(ContainSubstring("post-expansion data"))
	})

	It("should verify PV reflects expanded size", func() {
		ctx := context.Background()

		By("Creating PVC")
		pvcName := "expand-pv-check-iscsi"
		pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(context.Background(), pvc.Name)
		})

		By("Creating POD to trigger volume provisioning")
		podName := "expand-pv-check-pod-iscsi"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

		By("Getting initial PV capacity")
		pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())

		pv, err := f.K8s.GetPV(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		initialPVCapacity := pv.Spec.Capacity[corev1.ResourceStorage]

		By("Expanding PVC to 2Gi")
		err = f.K8s.ExpandPVC(ctx, pvc.Name, "2Gi")
		Expect(err).NotTo(HaveOccurred(), "Failed to request PVC expansion")

		By("Waiting for expansion to complete")
		err = f.K8s.WaitForPVCCapacity(ctx, pvc.Name, "2Gi", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "PVC expansion did not complete")

		By("Verifying PV capacity is updated")
		pv, err = f.K8s.GetPV(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		expandedPVCapacity := pv.Spec.Capacity[corev1.ResourceStorage]
		Expect(expandedPVCapacity.Cmp(initialPVCapacity)).To(BeNumerically(">", 0),
			"PV capacity should be greater than initial")
	})
})
