// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

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

		// Setup with "all" to enable NFS, NVMe-oF, and iSCSI storage classes
		err = f.Setup("all")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	// Test parameters for each protocol
	type protocolConfig struct {
		name          string // Display name for test output
		id            string // Lowercase identifier for K8s resource names (RFC 1123)
		protocol      string // Protocol parameter for StorageClass
		fsType        string // Filesystem type for block devices (empty for NFS)
		accessMode    corev1.PersistentVolumeAccessMode
		podTimeout    time.Duration
		needsPodFirst bool // NVMe-oF uses WaitForFirstConsumer
	}

	protocols := []protocolConfig{
		{
			name:          "NFS",
			id:            "nfs",
			protocol:      "nfs",
			fsType:        "", // NFS doesn't need fsType
			accessMode:    corev1.ReadWriteMany,
			podTimeout:    2 * time.Minute,
			needsPodFirst: false,
		},
		// NVMe-oF skipped - has issues with name templating and port binding
		// TODO: Re-enable once NVMe-oF subsystem port binding is fixed
		{
			name:          "iSCSI",
			id:            "iscsi",
			protocol:      "iscsi",
			fsType:        "ext4", // Block device needs filesystem
			accessMode:    corev1.ReadWriteOnce,
			podTimeout:    6 * time.Minute,
			needsPodFirst: true,
		},
	}

	for _, proto := range protocols {
		It("should create volumes with templated names from StorageClass parameters ["+proto.name+"]", func() {
			ctx := context.Background()
			scName := "tns-csi-" + proto.id + "-name-template"

			By("Creating StorageClass with nameTemplate parameter")
			params := map[string]string{
				"protocol":     proto.protocol,
				"pool":         f.Config.TrueNASPool,
				"server":       f.Config.TrueNASHost,
				"nameTemplate": "{{ .PVCNamespace }}-{{ .PVCName }}",
			}
			if proto.fsType != "" {
				params["fsType"] = proto.fsType
			}
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with nameTemplate")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(context.Background(), scName)
			})

			By("Creating PVC with templated StorageClass")
			pvcName := "name-template-test-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			// For NVMe-oF (WaitForFirstConsumer), we need to create pod first
			var pod *corev1.Pod
			if proto.needsPodFirst {
				By("Creating test pod (required for WaitForFirstConsumer binding)")
				pod, err = f.CreatePod(ctx, framework.PodOptions{
					Name:      "name-template-pod-" + proto.id,
					PVCName:   pvc.Name,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create pod")
			}

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

			// Create pod for NFS (not created above)
			if !proto.needsPodFirst {
				By("Creating test pod to verify volume works")
				pod, err = f.CreatePod(ctx, framework.PodOptions{
					Name:      "name-template-pod-" + proto.id,
					PVCName:   pvc.Name,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create pod")
			}

			By("Waiting for pod to be ready")
			err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
			Expect(err).NotTo(HaveOccurred(), "Pod did not become ready")

			By("Verifying I/O works on templated volume")
			_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'test data' > /data/test.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to write to volume")

			output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
			Expect(err).NotTo(HaveOccurred(), "Failed to read from volume")
			Expect(output).To(ContainSubstring("test data"))
		})

		It("should create volumes with prefix and suffix from StorageClass parameters ["+proto.name+"]", func() {
			ctx := context.Background()
			scName := "tns-csi-" + proto.id + "-prefix-suffix"

			By("Creating StorageClass with namePrefix and nameSuffix")
			params := map[string]string{
				"protocol":   proto.protocol,
				"pool":       f.Config.TrueNASPool,
				"server":     f.Config.TrueNASHost,
				"namePrefix": "prod-",
				"nameSuffix": "-data",
			}
			if proto.fsType != "" {
				params["fsType"] = proto.fsType
			}
			err := f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", params)
			Expect(err).NotTo(HaveOccurred(), "Failed to create StorageClass with prefix/suffix")
			f.Cleanup.Add(func() error {
				return f.K8s.DeleteStorageClass(context.Background(), scName)
			})

			By("Creating PVC with prefix/suffix StorageClass")
			pvcName := "prefix-suffix-test-" + proto.id
			pvc, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
				Name:             pvcName,
				StorageClassName: scName,
				Size:             "1Gi",
				AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create PVC")
			f.Cleanup.Add(func() error {
				return f.K8s.DeletePVC(context.Background(), pvc.Name)
			})

			// For NVMe-oF (WaitForFirstConsumer), we need to create pod first
			if proto.needsPodFirst {
				By("Creating test pod (required for WaitForFirstConsumer binding)")
				_, err = f.CreatePod(ctx, framework.PodOptions{
					Name:      "prefix-suffix-pod-" + proto.id,
					PVCName:   pvc.Name,
					MountPath: "/data",
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create pod")
			}

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
	}
})
