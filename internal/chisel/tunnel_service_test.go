package chisel

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TunnelService reverse remote authorization", func() {
	It("restricts agent reverse port to loopback and exact port", func() {
		re := allowedRemoteRegex(51001)
		Expect(re).To(Equal("^R:127.0.0.1:51001$"))

		rx := regexp.MustCompile(re)
		Expect(rx.MatchString("R:127.0.0.1:51001")).To(BeTrue())

		// Not loopback.
		Expect(rx.MatchString("R:0.0.0.0:51001")).To(BeFalse())
		Expect(rx.MatchString("R:127.0.0.2:51001")).To(BeFalse())

		// Wrong port.
		Expect(rx.MatchString("R:127.0.0.1:51002")).To(BeFalse())

		// No wildcards / extra segments.
		Expect(rx.MatchString("R:127.0.0.1:51001:127.0.0.1:9999")).To(BeFalse())
		Expect(rx.MatchString("xR:127.0.0.1:51001")).To(BeFalse())
		Expect(rx.MatchString("R:127.0.0.1:51001x")).To(BeFalse())
	})
})
