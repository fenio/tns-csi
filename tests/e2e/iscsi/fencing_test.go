package iscsi

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// The iSCSI suite deploys the driver with fencing.enabled (see framework/helm.go).
var _ = Describe("iSCSI Fencing", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")
		Expect(f.Setup("iscsi")).To(Succeed(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("admits only the node a volume is attached to", func() {
		ctx := context.Background()
		client := f.TrueNAS.Client()
		name := fmt.Sprintf("fencing-%d", time.Now().Unix())
		nodeGroup := "tns-csi:node:"
		if f.Config.ClusterID != "" {
			nodeGroup = "tns-csi:" + f.Config.ClusterID + ":node:"
		}

		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name: name, StorageClassName: "tns-csi-iscsi", Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPVCBound(ctx, pvc.Name, 3*time.Minute)).To(Succeed())
		pvc, err = f.K8s.GetPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		pvName := pvc.Spec.VolumeName

		// admitted returns the comments of the initiator groups the volume's target admits.
		admitted := func() []string {
			pv, pvErr := f.K8s.GetPV(ctx, pvName)
			Expect(pvErr).NotTo(HaveOccurred())
			iqn := pv.Spec.CSI.VolumeAttributes["iscsiIQN"]
			target, tErr := client.ISCSITargetByName(ctx, iqn[strings.LastIndex(iqn, ":")+1:])
			Expect(tErr).NotTo(HaveOccurred())
			Expect(target).NotTo(BeNil(), "target for %s", iqn)
			groups, gErr := client.QueryISCSIInitiators(ctx)
			Expect(gErr).NotTo(HaveOccurred())
			byID := map[int]tnsapi.ISCSIInitiator{}
			for _, g := range groups {
				byID[g.ID] = g
			}
			comments := make([]string, 0, len(target.Groups))
			for _, g := range target.Groups {
				comments = append(comments, byID[g.Initiator].Comment)
			}
			return comments
		}

		By("Checking a new volume admits no node before it is attached")
		Expect(admitted()).To(Equal([]string{"tns-csi:fenced"}))

		By("Attaching it to a pod")
		pod, err := f.CreatePod(ctx, framework.PodOptions{Name: name + "-pod", PVCName: pvc.Name, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, pod.Name, 3*time.Minute)).To(Succeed())
		pod, err = f.K8s.GetPod(ctx, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(admitted()).To(Equal([]string{nodeGroup + pod.Spec.NodeName}))
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo fenced > /data/f && sync"})
		Expect(err).NotTo(HaveOccurred())

		By("Detaching it and checking the node is fenced off")
		Expect(f.K8s.DeletePod(ctx, pod.Name)).To(Succeed())
		Expect(f.K8s.WaitForPodDeleted(ctx, pod.Name, 3*time.Minute)).To(Succeed())
		Expect(f.K8s.WaitForVolumeDetached(ctx, pvName, 3*time.Minute)).To(Succeed())
		Expect(admitted()).To(Equal([]string{"tns-csi:fenced"}))

		By("Attaching it again and reading the data back")
		pod, err = f.CreatePod(ctx, framework.PodOptions{Name: name + "-pod", PVCName: pvc.Name, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, pod.Name, 3*time.Minute)).To(Succeed())
		Expect(admitted()).To(Equal([]string{nodeGroup + pod.Spec.NodeName}))
		out, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/f"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("fenced"))
	})
})
