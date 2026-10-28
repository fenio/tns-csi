package crossnode

import (
	"context"
	"fmt"
	"os"
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

// preUpgradeVolume is a volume mounted while attachRequired was false.
type preUpgradeVolume struct {
	app, pvc, node string
	handle, pvName string
	inUseName      string // the kubelet's name for it in volumesInUse
	shell          string // node shell on node
}

// This spec checks docs/DEPLOYMENT.md "Volumes mounted before the upgrade are not yet
// fenced" against a real kubelet:
//   - a volume mounted while attachRequired was false is attached but NOT FENCED after
//     the upgrade (missing from node.status.volumesInUse);
//   - restarting its pod in place, as Deployments do, does not fix that;
//   - the VolumeAttachment of a NOT FENCED volume is deleted as soon as its pod is gone,
//     before the node has unstaged it, so it is no sign that scaling back up is safe;
//   - scaling to zero until the node has unstaged the volume, then back up, fences it;
//   - restarting the kubelet fences it without restarting the pod (needs a second node
//     and E2E_KUBELET_RESTART_CMD, the host command that restarts the kubelet; it runs
//     through kubectl exec, which the kubelet serves, so it must schedule the restart
//     and return, e.g. "systemd-run --on-active=3 systemctl restart kubelet").
var _ = Describe("Upgrade from a release without attachRequired", Ordered, func() {
	var (
		cfg     *framework.Config
		k8s     *framework.KubernetesClient
		scaled  *preUpgradeVolume // fixed by scaling to zero
		kubelet *preUpgradeVolume // fixed by restarting the kubelet; nil when not run
	)
	ctx := context.Background()
	const scName = "tns-csi-iscsi-previous"
	restartCmd := os.Getenv("E2E_KUBELET_RESTART_CMD")

	volumes := func() []*preUpgradeVolume {
		if kubelet == nil {
			return []*preUpgradeVolume{scaled}
		}
		return []*preUpgradeVolume{scaled, kubelet}
	}
	inUse := func(v *preUpgradeVolume) func() (bool, error) {
		return func() (bool, error) { return k8s.NodeVolumesInUse(ctx, v.node, v.inUseName) }
	}
	staged := func(v *preUpgradeVolume) func() (bool, error) {
		return func() (bool, error) {
			return k8s.NodeHasMount(ctx, v.shell, framework.CSIStagingDir(driverName, v.handle))
		}
	}

	BeforeAll(func() {
		f, err := framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		cfg = f.Config
		k8s, err = framework.NewKubernetesClient(cfg.Kubeconfig, fmt.Sprintf("e2e-upgrade-%d", time.Now().UnixNano()))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8s.CreateNamespace(ctx)).To(Succeed())
		DeferCleanup(func() { _ = k8s.DeleteNamespace(context.Background(), 5*time.Minute) })

		// Restarting a kubelet re-evaluates every volume on its node, so the two remedies
		// need volumes on different nodes. The kubelet restart goes to a node that is not
		// a control plane, so the API server stays up.
		names, err := k8s.SchedulableReadyNodes(ctx)
		Expect(err).NotTo(HaveOccurred())
		restartNode := ""
		if restartCmd != "" && len(names) > 1 {
			for _, n := range names {
				node, getErr := k8s.Clientset().CoreV1().Nodes().Get(ctx, n, metav1.GetOptions{})
				Expect(getErr).NotTo(HaveOccurred())
				if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; !cp {
					restartNode = n
					break
				}
			}
		}
		scaleNode := names[0]
		if scaleNode == restartNode {
			scaleNode = names[1]
		}
		scaled = &preUpgradeVolume{app: "upgrade-app", pvc: "upgrade-pvc", node: scaleNode}
		if restartNode != "" {
			kubelet = &preUpgradeVolume{app: "kubelet-app", pvc: "kubelet-pvc", node: restartNode}
		}
		for _, v := range volumes() {
			v.shell, err = k8s.StartNodeShell(ctx, v.node)
			Expect(err).NotTo(HaveOccurred())
		}
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

	It("mounts RWO iSCSI volumes in Deployments pinned to their nodes", func() {
		for _, v := range volumes() {
			_, err := k8s.CreatePVC(ctx, framework.PVCOptions{
				Name: v.pvc, StorageClassName: scName, Size: "1Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8s.CreateDeployment(ctx, framework.DeploymentOptions{
				Name: v.app, PVCName: v.pvc, NodeName: v.node, Replicas: 1,
			})).To(Succeed())
		}
		for _, v := range volumes() {
			pod, err := k8s.WaitForDeploymentPodRunning(ctx, v.app, "", 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo before-upgrade > /data/marker && sync"})
			Expect(err).NotTo(HaveOccurred())

			pv, err := k8s.GetPVForPVC(ctx, v.pvc)
			Expect(err).NotTo(HaveOccurred())
			v.pvName = pv.Name
			v.handle = pv.Spec.CSI.VolumeHandle
			v.inUseName = framework.CSIUniqueVolumeName(driverName, v.handle)

			attachments, err := k8s.GetVolumeAttachmentsForPV(ctx, v.pvName)
			Expect(err).NotTo(HaveOccurred())
			Expect(attachments).To(BeEmpty(), "attachRequired=false should create no VolumeAttachment")

			// Later steps wait for this mount to disappear; make sure the path is right.
			Expect(staged(v)()).To(BeTrue(), "no staging mount for %s on %s at %s",
				v.handle, v.node, framework.CSIStagingDir(driverName, v.handle))
		}
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

		By("Waiting for the attach/detach controller to attach the mounted volumes")
		for _, v := range volumes() {
			Expect(k8s.WaitForVolumeAttached(ctx, v.pvName, v.node, 6*time.Minute)).To(Succeed())
		}
	})

	It("leaves the pre-upgrade mounts NOT FENCED", func() {
		Consistently(func() (bool, error) {
			for _, v := range volumes() {
				if in, err := inUse(v)(); err != nil || in {
					return in, err
				}
			}
			return false, nil
		}, 45*time.Second, 5*time.Second).Should(BeFalse(),
			"the kubelet listed a pre-upgrade volume in volumesInUse; the docs' NOT FENCED section would be wrong")
	})

	It("is still NOT FENCED after restarting the pod in place", func() {
		pods, err := k8s.DeploymentPods(ctx, scaled.app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pods).To(HaveLen(1))
		old := pods[0]
		Expect(k8s.Clientset().CoreV1().Pods(k8s.Namespace()).Delete(ctx, old.Name, metav1.DeleteOptions{})).To(Succeed())
		pod, err := k8s.WaitForDeploymentPodRunning(ctx, scaled.app, old.UID, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod.Spec.NodeName).To(Equal(scaled.node))

		Consistently(inUse(scaled), 45*time.Second, 5*time.Second).Should(BeFalse(),
			"an in-place restart fenced the volume; docs/DEPLOYMENT.md says it does not")
	})

	It("is fenced after scaling to zero until the node has unstaged the volume", func() {
		Expect(k8s.ScaleDeployment(ctx, scaled.app, 0)).To(Succeed())
		Expect(k8s.WaitForDeploymentPodsGone(ctx, scaled.app, 3*time.Minute)).To(Succeed())

		// The attach/detach controller only waits for an unstage on volumes the node
		// lists in volumesInUse, so it deletes this VolumeAttachment right away. The node
		// may still have the volume staged at that point; scaling up then reuses the
		// kubelet's pre-upgrade entry and the volume stays NOT FENCED.
		Expect(k8s.WaitForVolumeDetached(ctx, scaled.pvName, 5*time.Minute)).To(Succeed())

		// docs/DEPLOYMENT.md has operators wait for the node plugin's logout line instead.
		By("Waiting for the node plugin to log out of the volume, as docs/DEPLOYMENT.md says")
		Eventually(func() (bool, error) {
			pods, err := k8s.GetPodsWithLabel(ctx, releaseNS, "app.kubernetes.io/component=node")
			if err != nil {
				return false, err
			}
			for i := range pods {
				if pods[i].Spec.NodeName != scaled.node {
					continue
				}
				logs, err := k8s.GetPodLogs(ctx, releaseNS, pods[i].Name, "tns-csi-plugin", 2000)
				if err != nil {
					return false, err
				}
				for _, line := range strings.Split(logs, "\n") {
					if strings.Contains(line, "Logged out iSCSI volume") && strings.Contains(line, scaled.pvName) {
						return true, nil
					}
				}
			}
			return false, nil
		}, 3*time.Minute, 2*time.Second).Should(BeTrue(), "the node plugin never logged out of %s", scaled.pvName)
		Expect(staged(scaled)()).To(BeFalse(),
			"the node plugin logged out of %s but the kubelet still has it staged", scaled.pvName)

		Expect(k8s.ScaleDeployment(ctx, scaled.app, 1)).To(Succeed())
		pod, err := k8s.WaitForDeploymentPodRunning(ctx, scaled.app, "", 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Eventually(inUse(scaled), 2*time.Minute, 5*time.Second).Should(BeTrue(),
			"scaling to zero until the volume was unstaged did not fence it")

		out, err := k8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/marker"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("before-upgrade"))
	})

	It("is fenced after restarting the kubelet, without restarting the pod", func() {
		if kubelet == nil {
			Skip("needs E2E_KUBELET_RESTART_CMD and a second schedulable node that is not a control plane")
		}
		before, err := k8s.WaitForDeploymentPodRunning(ctx, kubelet.app, "", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(inUse(kubelet)()).To(BeFalse(), "precondition: the volume should still be NOT FENCED")

		By("Restarting the kubelet on " + kubelet.node)
		out, err := k8s.RunOnHost(ctx, kubelet.shell, restartCmd)
		Expect(err).NotTo(HaveOccurred(), out)

		Eventually(inUse(kubelet), 4*time.Minute, 5*time.Second).Should(BeTrue(),
			"restarting the kubelet did not fence the volume")

		By("Checking the pod kept running with its data")
		after, err := k8s.WaitForDeploymentPodRunning(ctx, kubelet.app, "", 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.UID).To(Equal(before.UID), "the kubelet restart replaced the pod")
		for _, c := range after.Status.ContainerStatuses {
			Expect(c.RestartCount).To(BeZero(), "container %s restarted", c.Name)
		}
		out, err = k8s.ExecInPod(ctx, after.Name, []string{"sh", "-c", "cat /data/marker && echo after-restart > /data/marker2 && sync && cat /data/marker2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Fields(out)).To(Equal([]string{"before-upgrade", "after-restart"}))
	})

	AfterAll(func() {
		c := context.Background()
		for _, v := range volumes() {
			if v == nil {
				continue
			}
			_ = k8s.DeleteDeployment(c, v.app)
			_ = k8s.WaitForDeploymentPodsGone(c, v.app, 3*time.Minute)
			_ = k8s.DeletePVC(c, v.pvc)
			if v.pvName != "" {
				_ = k8s.WaitForPVDeleted(c, v.pvName, 3*time.Minute)
			}
		}
	})
})
