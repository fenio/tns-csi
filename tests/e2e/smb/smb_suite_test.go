// Package smb contains E2E tests for SMB protocol support.
package smb

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

func TestSMB(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SMB E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("smb")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
