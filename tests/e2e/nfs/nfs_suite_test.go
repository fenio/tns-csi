// Package nfs contains E2E tests for NFS protocol support.
package nfs

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

func TestNFS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NFS E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("nfs")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
