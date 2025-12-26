// Package nvmeof contains E2E tests for NVMe-oF protocol support.
package nvmeof

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

func TestNVMeoF(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NVMe-oF E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("nvmeof")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
