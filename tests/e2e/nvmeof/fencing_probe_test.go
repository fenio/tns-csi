package nvmeof

import (
	"context"
	"fmt"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// The fencing probe for #299: what happens to a connected host when the volume's subsystem
// stops allowing any host and allows only another one. See framework/fencing_probe.go.
var _ = Describe("NVMe-oF fencing-probe", func() {
	var f *framework.Framework

	BeforeEach(func() {
		if !framework.FencingProbeEnabled() {
			Skip("manual probe: set E2E_FENCING_PROBE=1")
		}
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Setup("nvmeof")).To(Succeed())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("records what revoking a connected host's access does", func() {
		ctx := context.Background()
		client := f.TrueNAS.Client()
		framework.ProbeMethods(ctx, client, "nvmet.global.", "nvmet.host", "nvmet.subsys.")

		pod, attrs, err := framework.ProbeMount(ctx, f, "tns-csi-nvmeof", fmt.Sprintf("fence-probe-nvmeof-%d", time.Now().Unix()))
		Expect(err).NotTo(HaveOccurred())
		subsysID, err := strconv.Atoi(attrs["nvmeofSubsystemID"])
		Expect(err).NotTo(HaveOccurred())
		_, _ = framework.ProbeCall(ctx, client, "nvmet.subsys.get_instance", subsysID)
		_, _ = framework.ProbeCall(ctx, client, "nvmet.host.query")

		By("Writing while any host is allowed")
		_, _ = framework.ProbeCall(ctx, client, "nvmet.global.sessions")
		framework.ProbeWrite(ctx, f, pod.Name, "before")

		By("Allowing only another host on the subsystem")
		res, err := framework.ProbeCall(ctx, client, "nvmet.host.create", map[string]any{
			"hostnqn": "nqn.2014-08.invalid.tns-csi:fencing-probe",
		})
		Expect(err).NotTo(HaveOccurred())
		hostID, err := framework.ProbeID(res)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _, _ = framework.ProbeCall(context.Background(), client, "nvmet.host.delete", hostID) })
		res, err = framework.ProbeCall(ctx, client, "nvmet.host_subsys.create", map[string]any{
			"host_id": hostID, "subsys_id": subsysID,
		})
		Expect(err).NotTo(HaveOccurred())
		linkID, err := framework.ProbeID(res)
		Expect(err).NotTo(HaveOccurred())
		// Removed before the host (cleanups run last-in first-out), and before the
		// subsystem is deleted on the normal path below.
		DeferCleanup(func() { _, _ = framework.ProbeCall(context.Background(), client, "nvmet.host_subsys.delete", linkID) })
		_, err = framework.ProbeCall(ctx, client, "nvmet.subsys.update", subsysID, map[string]any{"allow_any_host": false})
		Expect(err).NotTo(HaveOccurred())
		time.Sleep(10 * time.Second)

		By("Checking whether the existing connection survives and can still write")
		_, _ = framework.ProbeCall(ctx, client, "nvmet.global.sessions")
		framework.ProbeWrite(ctx, f, pod.Name, "after ACL change")

		By("Checking whether a new connection is refused")
		_, err = framework.ProbeRemount(ctx, f, pod, "new connect, access revoked", 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		_, _ = framework.ProbeCall(ctx, client, "nvmet.global.sessions")

		By("Restoring access and checking the volume mounts again")
		// The kernel refuses allow_any_host while the subsystem still lists hosts, so unlink first.
		_, err = framework.ProbeCall(ctx, client, "nvmet.host_subsys.delete", linkID)
		Expect(err).NotTo(HaveOccurred())
		_, err = framework.ProbeCall(ctx, client, "nvmet.subsys.update", subsysID, map[string]any{"allow_any_host": true})
		Expect(err).NotTo(HaveOccurred())
		ready, err := framework.ProbeRemount(ctx, f, pod, "access restored", 4*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue(), "the volume did not mount again after its access was restored")
	})
})
