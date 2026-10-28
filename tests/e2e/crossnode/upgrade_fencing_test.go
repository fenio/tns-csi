package crossnode

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// The last release that set CSIDriver.spec.attachRequired=false. Override with
// E2E_PREVIOUS_CHART_VERSION if it moves.
const previousChartVersion = "0.20.0"

const (
	driverName  = "tns.csi.io"
	releaseName = "tns-csi-driver"
	releaseNS   = "tns-csi"
	chartRepo   = "oci://registry-1.docker.io/bfenski/tns-csi-driver"
)

func helm(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
	return string(out), err
}

func kubectl(kubeconfig string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfig}, args...)...).CombinedOutput()
	return string(out), err
}

// This spec checks docs/DEPLOYMENT.md "Volumes mounted before the upgrade are not yet
// fenced" against a real kubelet:
//   - a volume mounted while attachRequired was false is attached but NOT FENCED after
//     the upgrade (missing from node.status.volumesInUse);
//   - restarting its pod in place, as Deployments do, does not fix that;
//   - scaling to zero until the VolumeAttachment is gone and scaling back up does.
var _ = Describe("Upgrade from a release without attachRequired", Ordered, func() {
	var (
		cfg       *framework.Config
		k8s       *framework.KubernetesClient
		node      string
		pvName    string
		inUseName string
	)
	ctx := context.Background()
	const app = "upgrade-app"
	const scName = "tns-csi-iscsi-previous"

	BeforeAll(func() {
		f, err := framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		cfg = f.Config
		k8s, err = framework.NewKubernetesClient(cfg.Kubeconfig, fmt.Sprintf("e2e-upgrade-%d", time.Now().UnixNano()))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8s.CreateNamespace(ctx)).To(Succeed())
		DeferCleanup(func() { _ = k8s.DeleteNamespace(context.Background(), 5*time.Minute) })

		nodes, err := k8s.SchedulableReadyNodes(ctx)
		Expect(err).NotTo(HaveOccurred())
		node = nodes[0]
	})

	It("installs the previous release with attachRequired=false", func() {
		By("Removing any installed release and its CSIDriver")
		_, _ = helm("uninstall", releaseName, "-n", releaseNS, "--wait")
		_, _ = kubectl(cfg.Kubeconfig, "delete", "csidriver", driverName, "--ignore-not-found")

		By("Installing chart " + previousChartVersion)
		storageClasses := fmt.Sprintf(`[{"name":%q,"enabled":true,"protocol":"iscsi","pool":%q,"server":%q,"port":"3260"}]`,
			scName, cfg.TrueNASPool, cfg.TrueNASHost)
		out, err := helm("install", releaseName, chartRepo, "--version", previousChartVersion,
			"-n", releaseNS, "--create-namespace", "--wait", "--timeout", "8m",
			"--set", "truenas.url=wss://"+cfg.TrueNASHost+"/api/current",
			"--set", "truenas.apiKey="+cfg.TrueNASAPIKey,
			"--set", "truenas.skipTLSVerify=true",
			"--set", "snapshots.enabled=false",
			"--set-json", "storageClasses="+storageClasses)
		Expect(err).NotTo(HaveOccurred(), out)

		d, err := k8s.GetCSIDriver(ctx, driverName)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Spec.AttachRequired).NotTo(BeNil())
		Expect(*d.Spec.AttachRequired).To(BeFalse(), "the previous release should not require attach")
	})

	It("mounts an RWO iSCSI volume in a Deployment pinned to one node", func() {
		_, err := k8s.CreatePVC(ctx, framework.PVCOptions{
			Name: "upgrade-pvc", StorageClassName: scName, Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8s.CreateDeployment(ctx, framework.DeploymentOptions{
			Name: app, PVCName: "upgrade-pvc", NodeName: node, Replicas: 1,
		})).To(Succeed())
		pod, err := k8s.WaitForDeploymentPodRunning(ctx, app, "", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo before-upgrade > /data/marker && sync"})
		Expect(err).NotTo(HaveOccurred())

		pv, err := k8s.GetPVForPVC(ctx, "upgrade-pvc")
		Expect(err).NotTo(HaveOccurred())
		pvName = pv.Name
		inUseName = framework.CSIUniqueVolumeName(driverName, pv.Spec.CSI.VolumeHandle)

		attachments, err := k8s.GetVolumeAttachmentsForPV(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(attachments).To(BeEmpty(), "attachRequired=false should create no VolumeAttachment")
	})

	It("upgrades to this build the way docs/DEPLOYMENT.md says", func() {
		By("Deleting the CSIDriver, which the chart's precheck requires")
		out, err := kubectl(cfg.Kubeconfig, "delete", "csidriver", driverName)
		Expect(err).NotTo(HaveOccurred(), out)

		By("Upgrading the release to this build")
		Expect(framework.NewHelmDeployer(cfg).Deploy("iscsi")).To(Succeed())
		d, err := k8s.GetCSIDriver(ctx, driverName)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Spec.AttachRequired).NotTo(BeNil())
		Expect(*d.Spec.AttachRequired).To(BeTrue())

		By("Waiting for the attach/detach controller to attach the mounted volume")
		Expect(k8s.WaitForVolumeAttached(ctx, pvName, node, 6*time.Minute)).To(Succeed())
	})

	It("leaves the pre-upgrade mount NOT FENCED", func() {
		Consistently(func() (bool, error) {
			return k8s.NodeVolumesInUse(ctx, node, inUseName)
		}, 45*time.Second, 5*time.Second).Should(BeFalse(),
			"the kubelet listed a pre-upgrade volume in volumesInUse; the docs' NOT FENCED section would be wrong")
	})

	It("is still NOT FENCED after restarting the pod in place", func() {
		pods, err := k8s.DeploymentPods(ctx, app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pods).To(HaveLen(1))
		old := pods[0]
		Expect(k8s.Clientset().CoreV1().Pods(k8s.Namespace()).Delete(ctx, old.Name, metav1.DeleteOptions{})).To(Succeed())
		pod, err := k8s.WaitForDeploymentPodRunning(ctx, app, old.UID, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod.Spec.NodeName).To(Equal(node))

		Consistently(func() (bool, error) {
			return k8s.NodeVolumesInUse(ctx, node, inUseName)
		}, 45*time.Second, 5*time.Second).Should(BeFalse(),
			"an in-place restart fenced the volume; docs/DEPLOYMENT.md says it does not")
	})

	It("is fenced after scaling to zero until the VolumeAttachment is gone", func() {
		Expect(k8s.ScaleDeployment(ctx, app, 0)).To(Succeed())
		Expect(k8s.WaitForDeploymentPodsGone(ctx, app, 3*time.Minute)).To(Succeed())
		Expect(k8s.WaitForVolumeDetached(ctx, pvName, 5*time.Minute)).To(Succeed())
		Eventually(func() (bool, error) {
			return k8s.NodeVolumesAttached(ctx, node, inUseName)
		}, 3*time.Minute, 5*time.Second).Should(BeFalse())

		Expect(k8s.ScaleDeployment(ctx, app, 1)).To(Succeed())
		pod, err := k8s.WaitForDeploymentPodRunning(ctx, app, "", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() (bool, error) {
			return k8s.NodeVolumesInUse(ctx, node, inUseName)
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "the documented remedy did not fence the volume")

		out, err := k8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/marker"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("before-upgrade"))
	})

	AfterAll(func() {
		c := context.Background()
		_ = k8s.DeleteDeployment(c, app)
		_ = k8s.WaitForDeploymentPodsGone(c, app, 3*time.Minute)
		_ = k8s.DeletePVC(c, "upgrade-pvc")
		if pvName != "" {
			_ = k8s.WaitForPVDeleted(c, pvName, 3*time.Minute)
		}
	})
})
