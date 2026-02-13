// Package providers aggregates all infrastructure-layer implementations
// (chisel, kubernetes, otterscale) into a single Wire provider set.
package providers

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/providers/kubernetes"
	"github.com/otterscale/otterscale-agent/internal/providers/otterscale"
)

// ProviderSet is the Wire provider set for all external adapters.
var ProviderSet = wire.NewSet(
	chisel.NewService,
	wire.Bind(new(core.TunnelProvider), new(*chisel.Service)),
	kubernetes.New,
	kubernetes.NewDiscoveryClient,
	kubernetes.NewResourceRepo,
	otterscale.NewFleetRegistrar,
)
