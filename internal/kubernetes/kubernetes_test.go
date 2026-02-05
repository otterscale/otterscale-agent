package kubernetes

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/otterscale/otterscale-agent/internal/config"
)

var _ = Describe("Kubernetes client impersonation guardrails", func() {
	It("refuses to create dynamic client when subject is missing from context", func() {
		k := New(config.New())
		_, err := k.dynamic(context.Background(), "dev")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("user sub not found in context"))
	})
})
