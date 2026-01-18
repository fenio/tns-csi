// Package iscsi contains E2E tests for iSCSI protocol support.
package iscsi

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

func TestISCSI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "iSCSI E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("iscsi")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
