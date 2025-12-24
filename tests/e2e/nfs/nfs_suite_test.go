// Package nfs contains E2E tests for NFS protocol support.
package nfs

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNFS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NFS E2E Suite")
}
