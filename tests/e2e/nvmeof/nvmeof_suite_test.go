// Package nvmeof contains E2E tests for NVMe-oF protocol support.
package nvmeof

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNVMeoF(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NVMe-oF E2E Suite")
}
