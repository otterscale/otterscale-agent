package impersonation

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestImpersonation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "internal/impersonation Suite")
}
