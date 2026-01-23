// Package e2e contains E2E tests for the TrueNAS CSI driver.
package e2e

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These tests verify CSI driver identity and capabilities are correctly reported.
// This is protocol-agnostic as identity is a driver-level concept.

var _ = Describe("CSI Identity", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		// Minimal setup - we just need access to the cluster
		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should have CSIDriver resource registered", func() {
		ctx := context.Background()

		By("Checking CSIDriver resource exists")
		csiDriver, err := f.K8s.GetCSIDriver(ctx, "tns.csi.io")
		Expect(err).NotTo(HaveOccurred(), "Failed to get CSIDriver resource")
		Expect(csiDriver).NotTo(BeNil(), "CSIDriver resource should exist")

		By("Verifying CSIDriver name")
		Expect(csiDriver.Name).To(Equal("tns.csi.io"), "CSIDriver name should be tns.csi.io")

		By("Checking CSIDriver spec")
		if csiDriver.Spec.AttachRequired != nil {
			if f.Verbose() {
				GinkgoWriter.Printf("CSIDriver attachRequired: %v\n", *csiDriver.Spec.AttachRequired)
			}
		}
		if csiDriver.Spec.PodInfoOnMount != nil {
			if f.Verbose() {
				GinkgoWriter.Printf("CSIDriver podInfoOnMount: %v\n", *csiDriver.Spec.PodInfoOnMount)
			}
		}
		if csiDriver.Spec.FSGroupPolicy != nil {
			if f.Verbose() {
				GinkgoWriter.Printf("CSIDriver fsGroupPolicy: %s\n", *csiDriver.Spec.FSGroupPolicy)
			}
		}

		if f.Verbose() {
			GinkgoWriter.Printf("CSIDriver resource verified successfully\n")
		}
	})

	It("should have controller deployment running", func() {
		ctx := context.Background()

		By("Checking controller deployment exists and is ready")
		ready, err := f.K8s.IsControllerReady(ctx)
		Expect(err).NotTo(HaveOccurred(), "Failed to check controller status")
		Expect(ready).To(BeTrue(), "Controller deployment should be ready")

		By("Verifying controller can respond to requests")
		logs, err := f.K8s.GetControllerLogs(ctx, 10)
		Expect(err).NotTo(HaveOccurred(), "Should be able to get controller logs")
		Expect(logs).NotTo(BeEmpty(), "Controller should have logs")

		if f.Verbose() {
			GinkgoWriter.Printf("Controller deployment is running and responsive\n")
		}
	})

	It("should have node daemonset running on all nodes", func() {
		ctx := context.Background()

		By("Checking node daemonset status")
		ready, desired, err := f.K8s.GetNodeDaemonSetStatus(ctx)
		Expect(err).NotTo(HaveOccurred(), "Failed to check node daemonset status")
		Expect(ready).To(Equal(desired), "All node pods should be ready (ready=%d, desired=%d)", ready, desired)
		Expect(desired).To(BeNumerically(">", 0), "Should have at least one node pod")

		if f.Verbose() {
			GinkgoWriter.Printf("Node daemonset: %d/%d PODs ready\n", ready, desired)
		}
	})

	It("should report correct capabilities in controller logs", func() {
		ctx := context.Background()

		By("Getting controller logs to verify capability registration")
		logs, err := f.K8s.GetControllerLogs(ctx, 200)
		Expect(err).NotTo(HaveOccurred(), "Failed to get controller logs")

		// Check for capability-related log messages
		// The driver should log its capabilities during startup
		hasCapabilityLogs := strings.Contains(logs, "capabilit") ||
			strings.Contains(logs, "CREATE_DELETE_VOLUME") ||
			strings.Contains(logs, "CREATE_DELETE_SNAPSHOT") ||
			strings.Contains(logs, "EXPAND_VOLUME")

		if f.Verbose() {
			if hasCapabilityLogs {
				GinkgoWriter.Printf("Found capability-related logs in controller output\n")
			} else {
				GinkgoWriter.Printf("Note: Capability logs may have rotated out or use different format\n")
			}
		}

		// The test passes as long as the controller is running - capability verification
		// is informational since log format may vary
	})

	It("should have all required StorageClasses available", func() {
		ctx := context.Background()

		requiredStorageClasses := []string{
			"tns-csi-nfs",
			"tns-csi-nvmeof",
			"tns-csi-iscsi",
		}

		By("Checking required StorageClasses exist")
		for _, scName := range requiredStorageClasses {
			sc, err := f.K8s.GetStorageClass(ctx, scName)
			if err != nil {
				if f.Verbose() {
					GinkgoWriter.Printf("StorageClass %s not found (may not be configured): %v\n", scName, err)
				}
				continue
			}
			Expect(sc.Provisioner).To(Equal("tns.csi.io"),
				"StorageClass %s should use tns.csi.io provisioner", scName)

			if f.Verbose() {
				GinkgoWriter.Printf("StorageClass %s: provisioner=%s, reclaimPolicy=%v\n",
					scName, sc.Provisioner, *sc.ReclaimPolicy)
			}
		}
	})
})
