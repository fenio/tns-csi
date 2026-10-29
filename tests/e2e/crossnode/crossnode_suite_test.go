// Package crossnode contains E2E specs that need at least two nodes: draining a node
// with an RWO block volume, and upgrading from a release without attachRequired.
package crossnode

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCrossNode(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cross-node E2E Suite")
}
