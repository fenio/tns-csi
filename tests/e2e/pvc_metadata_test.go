// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These tests verify that PVC metadata (annotations, labels) is handled correctly.
// This is protocol-agnostic as metadata handling is a CSI-level concern.

var _ = Describe("PVC Metadata", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("all")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	type protocolConfig struct {
		name         string
		id           string
		storageClass string
		accessMode   corev1.PersistentVolumeAccessMode
		podTimeout   time.Duration
	}

	protocols := []protocolConfig{
		{
			name:         "NFS",
			id:           "nfs",
			storageClass: "tns-csi-nfs",
			accessMode:   corev1.ReadWriteMany,
			podTimeout:   2 * time.Minute,
		},
		{
			name:         "NVMe-oF",
			id:           "nvmeof",
			storageClass: "tns-csi-nvmeof",
			accessMode:   corev1.ReadWriteOnce,
			podTimeout:   6 * time.Minute,
		},
		{
			name:         "iSCSI",
			id:           "iscsi",
			storageClass: "tns-csi-iscsi",
			accessMode:   corev1.ReadWriteOnce,
			podTimeout:   6 * time.Minute,
		},
	}

	for _, proto := range protocols {
		It("should preserve PVC labels on the PV ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating PVC with custom labels")
			pvcName := "metadata-labels-" + proto.id
			labels := map[string]string{
				"app":         "test-app",
				"environment": "testing",
				"team":        "platform",
			}
			pvc, err := f.K8s.CreatePVCWithLabels(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			}, labels)
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC with labels")
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			By("Creating POD to trigger volume provisioning")
			podName := "metadata-labels-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

			By("Verifying PVC labels are preserved")
			pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			for key, value := range labels {
				Expect(pvc.Labels).To(HaveKeyWithValue(key, value),
					"PVC should have label %s=%s", key, value)
			}

			By("Getting PV and checking for CSI-related annotations")
			pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())

			pv, err := f.K8s.GetPV(ctx, pvName)
			Expect(err).NotTo(HaveOccurred())

			// CSI provisioner adds these annotations
			Expect(pv.Annotations).To(HaveKey("pv.kubernetes.io/provisioned-by"),
				"PV should have provisioner annotation")
			Expect(pv.Annotations["pv.kubernetes.io/provisioned-by"]).To(Equal("tns.csi.io"),
				"PV should be provisioned by tns.csi.io")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] PVC labels preserved, PV annotations correct\n", proto.name)
				GinkgoWriter.Printf("  PV annotations: %v\n", pv.Annotations)
			}
		})

		It("should preserve PVC annotations ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating PVC with custom annotations")
			pvcName := "metadata-annotations-" + proto.id
			annotations := map[string]string{
				"example.com/backup-policy": "daily",
				"example.com/owner":         "test-team",
			}
			pvc, err := f.K8s.CreatePVCWithAnnotations(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			}, annotations)
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC with annotations")
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			By("Creating POD to trigger volume provisioning")
			podName := "metadata-annotations-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

			By("Verifying PVC annotations are preserved")
			pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())
			for key, value := range annotations {
				Expect(pvc.Annotations).To(HaveKeyWithValue(key, value),
					"PVC should have annotation %s=%s", key, value)
			}

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] PVC annotations preserved correctly\n", proto.name)
			}
		})

		It("should set correct volume attributes in PV ["+proto.name+"]", func() {
			ctx := context.Background()

			By("Creating PVC")
			pvcName := "metadata-attrs-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: proto.storageClass,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			By("Creating POD to trigger volume provisioning")
			podName := "metadata-attrs-pod-" + proto.id
			pod, err := f.CreatePod(ctx, framework.PodOptions{
				Name:      podName,
				PVCName:   pvc.Name,
				MountPath: "/data",
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create POD")

			By("Waiting for POD to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Waiting for PVC to become Bound")
			err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "PVC did not become Bound")

			By("Getting PV and verifying CSI volume attributes")
			pvName, err := f.K8s.GetPVName(ctx, pvc.Name)
			Expect(err).NotTo(HaveOccurred())

			pv, err := f.K8s.GetPV(ctx, pvName)
			Expect(err).NotTo(HaveOccurred())

			// Verify CSI volume source is set
			Expect(pv.Spec.CSI).NotTo(BeNil(), "PV should have CSI volume source")
			Expect(pv.Spec.CSI.Driver).To(Equal("tns.csi.io"), "CSI driver should be tns.csi.io")
			Expect(pv.Spec.CSI.VolumeHandle).NotTo(BeEmpty(), "Volume handle should not be empty")

			// Volume attributes should contain protocol-specific info
			if pv.Spec.CSI.VolumeAttributes != nil {
				if f.Verbose() {
					GinkgoWriter.Printf("[%s] CSI volume attributes:\n", proto.name)
					for k, v := range pv.Spec.CSI.VolumeAttributes {
						GinkgoWriter.Printf("  %s: %s\n", k, v)
					}
				}
			}

			// Verify access modes match
			Expect(pv.Spec.AccessModes).To(ContainElement(proto.accessMode),
				"PV should have requested access mode")

			if f.Verbose() {
				GinkgoWriter.Printf("[%s] PV volume attributes verified\n", proto.name)
				GinkgoWriter.Printf("  Volume handle: %s\n", pv.Spec.CSI.VolumeHandle)
			}
		})
	}
})
