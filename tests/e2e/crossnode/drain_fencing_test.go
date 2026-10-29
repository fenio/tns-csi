package crossnode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// With attachRequired=true, draining a node must move an RWO block volume without it
// ever being attached to two nodes at once (#291). The Deployment's replacement pod is
// created as soon as the old one starts terminating, which is exactly the race that
// corrupted ext4 before #295; the attach/detach controller has to hold the new attach
// until the old node has unstaged.
var _ = Describe("Draining a node with an RWO block volume", Ordered, func() {
	var (
		f        *framework.Framework
		nodes    []string
		cordoned string
	)
	ctx := context.Background()

	BeforeAll(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Setup("iscsi")).To(Succeed())
		nodes, err = f.K8s.SchedulableReadyNodes(ctx)
		Expect(err).NotTo(HaveOccurred())
		if len(nodes) < 2 {
			Skip(fmt.Sprintf("needs two schedulable nodes, found %v", nodes))
		}
	})

	AfterAll(func() {
		if cordoned != "" {
			_ = f.K8s.SetNodeUnschedulable(context.Background(), cordoned, false)
		}
		if f != nil {
			_ = f.K8s.DeleteDeployment(context.Background(), "drain-app")
			_ = f.K8s.WaitForDeploymentPodsGone(context.Background(), "drain-app", 3*time.Minute)
			f.Teardown()
		}
	})

	It("never attaches the volume to two nodes and keeps the data", func() {
		const app = "drain-app"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name: "drain-pvc", StorageClassName: "tns-csi-iscsi", Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())

		By("Running a writer that appends to the volume every half second")
		Expect(f.K8s.CreateDeployment(ctx, framework.DeploymentOptions{
			Name: app, PVCName: pvc.Name, Replicas: 1,
			Command: []string{"sh", "-c", `while true; do echo "$(hostname) $(date +%s%N)" >> /data/log; sync; sleep 0.5; done`},
		})).To(Succeed())
		first, err := f.K8s.WaitForDeploymentPodRunning(ctx, app, "", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		source := first.Spec.NodeName
		pv, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		inUse := framework.CSIUniqueVolumeName("tns.csi.io", pv.Spec.CSI.VolumeHandle)

		By("Checking the volume is attached to and in use on " + source)
		Expect(f.K8s.WaitForVolumeAttached(ctx, pv.Name, source, 2*time.Minute)).To(Succeed())
		Eventually(func() (bool, error) {
			return f.K8s.NodeVolumesInUse(ctx, source, inUse)
		}, time.Minute, 2*time.Second).Should(BeTrue(), "a freshly mounted volume must be in volumesInUse, or nothing fences it")
		time.Sleep(3 * time.Second) // a few lines from the first pod

		By("Watching VolumeAttachments for overlap while the node drains")
		var (
			mu       sync.Mutex
			maxNodes int
			overlap  []string
		)
		watchCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			defer GinkgoRecover()
			for watchCtx.Err() == nil {
				vas, vaErr := f.K8s.GetVolumeAttachmentsForPV(watchCtx, pv.Name)
				if vaErr == nil {
					var attached []string
					for i := range vas {
						if vas[i].Status.Attached {
							attached = append(attached, vas[i].Spec.NodeName)
						}
					}
					mu.Lock()
					if len(attached) > maxNodes {
						maxNodes = len(attached)
					}
					if len(attached) > 1 {
						overlap = attached
					}
					mu.Unlock()
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()

		By("Draining " + source + ": cordon, then evict the pod")
		Expect(f.K8s.SetNodeUnschedulable(ctx, source, true)).To(Succeed())
		cordoned = source
		Expect(f.K8s.EvictPod(ctx, first.Name)).To(Succeed())

		second, err := f.K8s.WaitForDeploymentPodRunning(ctx, app, first.UID, 10*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		target := second.Spec.NodeName
		Expect(target).NotTo(Equal(source))
		Expect(f.K8s.WaitForVolumeAttached(ctx, pv.Name, target, 2*time.Minute)).To(Succeed())
		time.Sleep(3 * time.Second) // a few lines from the second pod
		stop()

		mu.Lock()
		Expect(overlap).To(BeEmpty(), "the RWO volume was attached to %v at the same time", overlap)
		Expect(maxNodes).To(Equal(1))
		mu.Unlock()

		By("Checking the log has both pods' writes and the filesystem is intact")
		out, err := f.K8s.ExecInPod(ctx, second.Name, []string{"cat", "/data/log"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(first.Name))
		Expect(out).To(ContainSubstring(second.Name))
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			Expect(strings.Fields(line)).To(HaveLen(2), "corrupt line %q", line)
		}
	})
})
