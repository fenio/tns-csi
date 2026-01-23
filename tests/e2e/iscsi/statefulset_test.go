// Package iscsi contains E2E tests for iSCSI volumes.
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

var _ = Describe("iSCSI StatefulSet", func() {
	var f *framework.Framework
	var ctx context.Context

	const (
		stsName          = "web-iscsi"
		serviceName      = "web-iscsi-svc"
		replicas         = int32(2)
		volumeName       = "data" // Name in volumeClaimTemplates
		mountPath        = "/data"
		storageSize      = "1Gi"
		storageClassName = "tns-csi-iscsi"
		// iSCSI uses WaitForFirstConsumer, so longer timeouts needed
		podTimeout = 180 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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

	It("should support StatefulSet with volumeClaimTemplates", func() {
		By("Creating headless service")
		labels := map[string]string{"app": stsName}
		err := f.K8s.CreateHeadlessService(ctx, serviceName, labels)
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteService(ctx, serviceName)
		})

		By(fmt.Sprintf("Creating StatefulSet with %d replicas", replicas))
		err = f.K8s.CreateStatefulSet(ctx, framework.StatefulSetOptions{
			Name:             stsName,
			ServiceName:      serviceName,
			Replicas:         replicas,
			StorageClassName: storageClassName,
			StorageSize:      storageSize,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Labels:           labels,
			MountPath:        mountPath,
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStatefulSet(ctx, stsName)
		})

		// Register PVC cleanup (StatefulSet creates data-<sts>-<n> PVCs)
		for i := range replicas {
			pvcName := f.K8s.GetStatefulSetPVCName(stsName, volumeName, int(i))
			// Capture the pvcName in the closure
			name := pvcName
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(ctx, name)
			})
		}

		By("Waiting for all PODs to be ready")
		// iSCSI uses WaitForFirstConsumer, so PVCs bind when pods are scheduled
		err = f.K8s.WaitForStatefulSetReady(ctx, stsName, replicas, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying each POD has unique PVC bound")
		for i := range replicas {
			pvcName := f.K8s.GetStatefulSetPVCName(stsName, volumeName, int(i))
			pvc, pvcErr := f.K8s.GetPVC(ctx, pvcName)
			Expect(pvcErr).NotTo(HaveOccurred())
			Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound),
				fmt.Sprintf("PVC %s should be bound", pvcName))
		}

		By("Verifying data isolation between replicas")
		// Each pod writes its identity via the StatefulSet command
		for i := range replicas {
			podName := f.K8s.GetStatefulSetPodName(stsName, int(i))
			output, execErr := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/pod-identity.txt"})
			Expect(execErr).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("Pod: "+podName),
				fmt.Sprintf("Pod %s should have correct identity", podName))
		}

		By("Writing unique test data to each replica")
		for i := range replicas {
			podName := f.K8s.GetStatefulSetPodName(stsName, int(i))
			// Use sync for iSCSI to ensure data is written to disk
			_, execErr := f.K8s.ExecInPod(ctx, podName, []string{
				"sh", "-c", fmt.Sprintf("echo 'Unique data for replica %d' > /data/replica-data.txt && sync", i),
			})
			Expect(execErr).NotTo(HaveOccurred())
		}

		By("Scaling down StatefulSet from 2 to 1 replica")
		newReplicas := int32(1)
		err = f.K8s.ScaleStatefulSet(ctx, stsName, newReplicas)
		Expect(err).NotTo(HaveOccurred())

		// Wait for the last pod to be deleted
		deletedPod := f.K8s.GetStatefulSetPodName(stsName, int(replicas-1))
		err = f.K8s.WaitForPodToBeDeleted(ctx, deletedPod, 120*time.Second)
		Expect(err).NotTo(HaveOccurred())

		// Give system time to settle after pod deletion (iSCSI device cleanup)
		time.Sleep(5 * time.Second)

		By("Verifying remaining PODs retained their data")
		for i := range newReplicas {
			podName := f.K8s.GetStatefulSetPodName(stsName, int(i))
			output, execErr := f.K8s.ExecInPod(ctx, podName, []string{"cat", "/data/replica-data.txt"})
			Expect(execErr).NotTo(HaveOccurred())
			Expect(output).To(Equal(fmt.Sprintf("Unique data for replica %d", i)),
				fmt.Sprintf("Pod %s should retain data after scale down", podName))
		}

		By("Verifying PVC for scaled-down POD is retained")
		scaledDownPVC := f.K8s.GetStatefulSetPVCName(stsName, volumeName, int(replicas-1))
		pvc, err := f.K8s.GetPVC(ctx, scaledDownPVC)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil(), "PVC should be retained after scale down (StatefulSet behavior)")

		By("Scaling back up to 2 replicas")
		err = f.K8s.ScaleStatefulSet(ctx, stsName, replicas)
		Expect(err).NotTo(HaveOccurred())

		// Wait for the scaled-up pod to be ready
		scaledUpPod := f.K8s.GetStatefulSetPodName(stsName, int(replicas-1))
		err = f.K8s.WaitForPodReady(ctx, scaledUpPod, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying scaled-up POD reattached to original volume with preserved data")
		output, err := f.K8s.ExecInPod(ctx, scaledUpPod, []string{"cat", "/data/pod-identity.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("Pod: "+scaledUpPod),
			"Scaled-up pod should reattach to original volume")

		By("Testing rolling update - deleting POD and waiting for recreation")
		testPod := f.K8s.GetStatefulSetPodName(stsName, 1)
		err = f.K8s.DeletePod(ctx, testPod)
		Expect(err).NotTo(HaveOccurred())

		// Wait for StatefulSet controller to recreate the pod
		err = f.K8s.WaitForPodReady(ctx, testPod, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying recreated POD has original data")
		output, err = f.K8s.ExecInPod(ctx, testPod, []string{"cat", "/data/replica-data.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("Unique data for replica 1"),
			"Recreated pod should have original data")
	})
})
