// Package encryption contains E2E tests for ZFS native encryption support.
// These tests verify that encrypted volumes work correctly for both NFS and NVMe-oF protocols.
package encryption

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

func TestEncryption(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Encryption E2E Suite")
}

var _ = BeforeSuite(func() {
	// Setup with "both" protocol to enable both NFS and NVMe-oF storage classes
	err := framework.SetupSuite("both")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
