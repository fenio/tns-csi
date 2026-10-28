package nfs

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

const (
	controllerDeployment = "tns-csi-driver-controller"
	driverNamespace      = "tns-csi"
	snapshotterContainer = "csi-snapshotter"
)

// setSnapshotterTimeout replaces the csi-snapshotter sidecar's --timeout and waits for
// the controller to roll out. It returns the previous value ("" if the flag was unset).
func setSnapshotterTimeout(ctx context.Context, f *framework.Framework, timeout string) string {
	deployments := f.K8s.Clientset().AppsV1().Deployments(driverNamespace)
	d, err := deployments.Get(ctx, controllerDeployment, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	previous := ""
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]
		if c.Name != snapshotterContainer {
			continue
		}
		var args []string
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--timeout="); ok {
				previous = v
				continue
			}
			args = append(args, a)
		}
		if timeout != "" {
			args = append(args, "--timeout="+timeout)
		}
		c.Args = args
	}
	_, err = deployments.Update(ctx, d, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(waitForRollout(ctx, f)).To(Succeed())
	return previous
}

func waitForRollout(ctx context.Context, f *framework.Framework) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		d, err := f.K8s.Clientset().AppsV1().Deployments(driverNamespace).Get(ctx, controllerDeployment, metav1.GetOptions{})
		return err == nil && rolledOut(d), nil // a transient error keeps polling
	})
}

// snapshotContentError returns status.error.message of the VolumeSnapshotContent bound
// to the named VolumeSnapshot, or "" when there is none yet.
func snapshotContentError(ctx context.Context, f *framework.Framework, snapshot string) string {
	kubectl := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", f.Config.Kubeconfig}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	content := kubectl("get", "volumesnapshot", snapshot, "-n", f.Namespace(), "-o", "jsonpath={.status.boundVolumeSnapshotContentName}")
	if content == "" {
		return ""
	}
	return kubectl("get", "volumesnapshotcontent", content, "-o", "jsonpath={.status.error.message}")
}

func rolledOut(d *appsv1.Deployment) bool {
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas == *d.Spec.Replicas &&
		d.Status.AvailableReplicas == *d.Spec.Replicas &&
		d.Status.Replicas == *d.Spec.Replicas
}

// A detached snapshot copies the volume with a TrueNAS replication. When the copy takes
// longer than the snapshotter's timeout, the external-snapshotter retries CreateSnapshot
// while the replication is still running. The retry must wait for the copy (or redo it)
// and must not report the snapshot ready while its dataset is partial; restoring from it
// has to give back exactly the source's data.
var _ = Describe("Detached snapshot retried after a timeout", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Setup("nfs")).To(Succeed())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("is ready only once the copy is complete, and restores the full data", func() {
		ctx := context.Background()

		By("Creating a source volume with 3 GiB of random data")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name: "snap-retry-src", StorageClassName: "tns-csi-nfs", Size: "8Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		pod, err := f.CreatePod(ctx, framework.PodOptions{Name: "snap-retry-src-pod", PVCName: pvc.Name, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)).To(Succeed())
		sum := writeRandomData(ctx, f, pod.Name, 3072)

		By("Shortening the snapshotter timeout so the first CreateSnapshot times out mid-copy")
		previous := setSnapshotterTimeout(ctx, f, "3s")
		DeferCleanup(func() { setSnapshotterTimeout(context.Background(), f, previous) })

		By("Taking a detached snapshot")
		Expect(f.K8s.CreateVolumeSnapshotClassWithParams(ctx, "snap-retry-class", "tns.csi.io", "Delete",
			map[string]string{"detachedSnapshots": "true"})).To(Succeed())
		f.Cleanup.Add(func() error { return f.K8s.DeleteVolumeSnapshotClass(context.Background(), "snap-retry-class") })
		Expect(f.K8s.CreateVolumeSnapshot(ctx, "snap-retry", pvc.Name, "snap-retry-class")).To(Succeed())
		f.Cleanup.Add(func() error { return f.K8s.DeleteVolumeSnapshot(context.Background(), "snap-retry") })

		// The external-snapshotter records a timed-out CreateSnapshot on the content's
		// status.error before retrying. Watch for it while the snapshot settles: it proves
		// the retry path ran, independently of the driver's own logging.
		var (
			mu        sync.Mutex
			timedOut  bool
			lastError string
		)
		watchCtx, stopWatch := context.WithCancel(ctx)
		go func() {
			defer GinkgoRecover()
			for watchCtx.Err() == nil {
				if msg := snapshotContentError(watchCtx, f, "snap-retry"); msg != "" {
					mu.Lock()
					lastError = msg
					if strings.Contains(strings.ToLower(msg), "deadline") {
						timedOut = true
					}
					mu.Unlock()
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
		Expect(f.K8s.WaitForSnapshotReady(ctx, "snap-retry", 15*time.Minute)).To(Succeed())
		stopWatch()

		By("Checking the first attempt really timed out and was retried")
		mu.Lock()
		Expect(timedOut).To(BeTrue(),
			"precondition: no CreateSnapshot timeout was recorded (last content error %q); the copy finished inside the timeout, so the retry path was not exercised", lastError)
		mu.Unlock()

		By("Restoring from the snapshot and comparing the data")
		Expect(f.K8s.CreatePVCFromSnapshot(ctx, "snap-retry-restored", "snap-retry", "tns-csi-nfs", "8Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})).To(Succeed())
		f.RegisterPVCCleanup("snap-retry-restored")
		rpod, err := f.CreatePod(ctx, framework.PodOptions{Name: "snap-retry-restored-pod", PVCName: "snap-retry-restored", MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, rpod.Name, 5*time.Minute)).To(Succeed())
		out, err := f.K8s.ExecInPod(ctx, rpod.Name, []string{"sh", "-c", "md5sum /data/blob | cut -d' ' -f1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal(sum), "the snapshot was reported ready before its copy was complete")
	})
})
