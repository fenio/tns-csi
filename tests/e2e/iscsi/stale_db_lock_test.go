package iscsi

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// Regression test for #336. open-iscsi takes its node DB lock by hard-linking lock to
// lock.write and drops it by unlinking lock.write on a clean exit. An iscsiadm killed while
// holding it leaves lock.write behind, and every later iscsiadm that touches the DB waits
// 30 s and fails, so the node cannot stage any iSCSI volume until someone deletes the file.
//
// This spec puts a node in exactly that state (lock.write a hard link of lock, link count 2,
// no live owner, older than the driver's 60 s stale threshold) and checks that the driver
// clears it and stages a volume on that node. It runs against the host's real lock
// directory, so it also catches lock-directory layouts the unit tests' fake roots miss.
var _ = Describe("iSCSI orphaned node DB lock (#336)", func() {
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

	It("clears an orphaned lock.write and stages a volume on that node", func() {
		ctx := context.Background()
		stamp := time.Now().Unix()

		nodes, err := f.K8s.SchedulableReadyNodes(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(nodes).NotTo(BeEmpty())
		node := nodes[0]

		By("Opening a shell on node " + node)
		shell, err := f.K8s.StartNodeShell(ctx, node)
		Expect(err).NotTo(HaveOccurred(), "Failed to start node shell")
		onHost := func(cmd string) (string, error) {
			out, runErr := f.K8s.RunOnHost(ctx, shell, cmd)
			return strings.TrimSpace(out), runErr
		}

		By("Orphaning open-iscsi's node DB lock the way a killed iscsiadm does")
		// Same directory order as the driver: /run/lock/iscsi, else /var/lock/iscsi.
		lockDir, err := onHost(`for d in /run/lock/iscsi /var/lock/iscsi; do [ -d "$d" ] && { readlink -f "$d"; exit 0; }; done; ` +
			`mkdir -p /run/lock/iscsi && echo /run/lock/iscsi`)
		Expect(err).NotTo(HaveOccurred())
		writeLock := lockDir + "/lock.write"
		// ln fails while a real iscsiadm holds the lock; wait for it to let go.
		var planted string
		Eventually(func() error {
			out, plantErr := onHost(fmt.Sprintf(`[ -e %[1]s/lock ] || : > %[1]s/lock; ln %[1]s/lock %[2]s && stat -c %%Z %[2]s`, lockDir, writeLock))
			planted = out
			return plantErr
		}, 2*time.Minute, 2*time.Second).Should(Succeed(), "could not take the lock to orphan it")
		DeferCleanup(func() {
			// Only remove the file this spec planted, never a lock a real iscsiadm took since.
			_, _ = onHost(fmt.Sprintf(`[ "$(stat -c %%Z %[1]s 2>/dev/null)" = %[2]s ] && rm -f %[1]s; true`, writeLock, planted))
		})
		links, err := onHost("stat -c %h " + lockDir + "/lock")
		Expect(err).NotTo(HaveOccurred())
		Expect(links).To(Equal("2"), "lock.write must be a hard link of lock, as in #336")
		GinkgoWriter.Printf("Planted %s on %s (ctime %s)\n", writeLock, node, planted)

		By("Letting the lock age past the driver's 60 s stale threshold")
		plantedAt, err := strconv.ParseInt(planted, 10, 64)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() (int64, error) {
			now, nowErr := onHost("date +%s")
			if nowErr != nil {
				return 0, nowErr
			}
			n, parseErr := strconv.ParseInt(now, 10, 64)
			return n - plantedAt, parseErr
		}, 3*time.Minute, 5*time.Second).Should(BeNumerically(">", 65))
		logsSince := metav1.Now()

		By("Staging an iSCSI volume on the same node")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             fmt.Sprintf("stale-lock-pvc-%d", stamp),
			StorageClassName: "tns-csi-iscsi",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)).To(Succeed())
		app := fmt.Sprintf("stale-lock-app-%d", stamp)
		Expect(f.K8s.CreateDeployment(ctx, framework.DeploymentOptions{
			Name: app, PVCName: pvc.Name, NodeName: node, Replicas: 1,
		})).To(Succeed())
		// Cleanups run last-in first-out: remove the pod before the PVC cleanup waits on it.
		f.DeferCleanup(func() error {
			cleanupCtx := context.Background()
			if delErr := f.K8s.DeleteDeployment(cleanupCtx, app); delErr != nil {
				return delErr
			}
			return f.K8s.WaitForDeploymentPodsGone(cleanupCtx, app, 4*time.Minute)
		})
		// With the lock left in place every node DB operation fails after open-iscsi's 30 s
		// wait and the pod never starts.
		_, err = f.K8s.WaitForDeploymentPodRunning(ctx, app, "", 3*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "pod using the volume did not start: the orphaned lock was not cleared")

		By("Checking the driver removed the planted lock and said so")
		current, err := onHost(fmt.Sprintf(`stat -c %%Z %s 2>/dev/null || true`, writeLock))
		Expect(err).NotTo(HaveOccurred())
		Expect(current).NotTo(Equal(planted), "the planted lock.write is still there")
		logs, err := f.K8s.NodePluginLogs(ctx, node, logsSince)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("Removed orphaned open-iscsi DB lock"))
		// A healthy host must not be reported as one the janitor cannot inspect.
		Expect(logs).NotTo(ContainSubstring("open-iscsi DB lock check: cannot"))
	})
})
