package iscsi

import (
	"context"
	"fmt"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// The fencing probe for #299: what happens to a logged-in node when the volume's target is
// moved to an initiator group that does not include it. See framework/fencing_probe.go.
var _ = Describe("iSCSI fencing-probe", func() {
	var f *framework.Framework

	BeforeEach(func() {
		if !framework.FencingProbeEnabled() {
			Skip("manual probe: set E2E_FENCING_PROBE=1")
		}
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Setup("iscsi")).To(Succeed())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("records what revoking a logged-in initiator's access does", func() {
		ctx := context.Background()
		client := f.TrueNAS.Client()
		framework.ProbeMethods(ctx, client, "iscsi.global.", "iscsi.initiator.", "iscsi.target.")

		pod, attrs, err := framework.ProbeMount(ctx, f, "tns-csi-iscsi", fmt.Sprintf("fence-probe-iscsi-%d", time.Now().Unix()))
		Expect(err).NotTo(HaveOccurred())
		targetID, err := strconv.Atoi(attrs["iscsiTargetID"])
		Expect(err).NotTo(HaveOccurred())
		targets, err := client.QueryISCSITargets(ctx, []any{[]any{"id", "=", targetID}})
		Expect(err).NotTo(HaveOccurred())
		Expect(targets).To(HaveLen(1))
		original := targets[0].Groups
		Expect(original).NotTo(BeEmpty())
		_, _ = framework.ProbeCall(ctx, client, "iscsi.initiator.query")

		By("Writing while the node has access")
		_, _ = framework.ProbeCall(ctx, client, "iscsi.global.sessions")
		framework.ProbeWrite(ctx, f, pod.Name, "before")

		By("Moving the target to an initiator group without this node")
		res, err := framework.ProbeCall(ctx, client, "iscsi.initiator.create", map[string]any{
			"initiators": []string{"iqn.2026-10.invalid.tns-csi:fencing-probe"},
			"comment":    "tns-csi fencing probe",
		})
		Expect(err).NotTo(HaveOccurred())
		groupID, err := framework.ProbeID(res)
		Expect(err).NotTo(HaveOccurred())
		// Runs after Teardown has deleted the target that refers to the group.
		DeferCleanup(func() { _, _ = framework.ProbeCall(context.Background(), client, "iscsi.initiator.delete", groupID) })

		groups := func(initiator int) []map[string]any {
			out := make([]map[string]any, 0, len(original))
			for _, g := range original {
				out = append(out, map[string]any{"portal": g.Portal, "initiator": initiator, "authmethod": "NONE", "auth": nil})
			}
			return out
		}
		_, err = framework.ProbeCall(ctx, client, "iscsi.target.update", targetID, map[string]any{"groups": groups(groupID)})
		Expect(err).NotTo(HaveOccurred())
		time.Sleep(10 * time.Second)

		By("Checking whether the existing session survives and can still write")
		_, _ = framework.ProbeCall(ctx, client, "iscsi.global.sessions")
		framework.ProbeWrite(ctx, f, pod.Name, "after ACL change")

		By("Reloading the iSCSI service, as the driver does after target changes")
		GinkgoWriter.Printf("PROBE reload: %v\n", client.ReloadISCSIService(ctx))
		time.Sleep(10 * time.Second)
		_, _ = framework.ProbeCall(ctx, client, "iscsi.global.sessions")
		framework.ProbeWrite(ctx, f, pod.Name, "after reload")

		By("Checking whether a new login is refused")
		_, err = framework.ProbeRemount(ctx, f, pod, "new login, access revoked", 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		_, _ = framework.ProbeCall(ctx, client, "iscsi.global.sessions")

		By("Restoring access and checking the volume mounts again")
		_, err = framework.ProbeCall(ctx, client, "iscsi.target.update", targetID, map[string]any{"groups": groups(original[0].Initiator)})
		Expect(err).NotTo(HaveOccurred())
		ready, err := framework.ProbeRemount(ctx, f, pod, "access restored", 4*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue(), "the volume did not mount again after its access was restored")
	})
})
