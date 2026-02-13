package providers

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/providers/kubernetes"
	"github.com/otterscale/otterscale-agent/internal/providers/otterscale"
)

var ProviderSet = wire.NewSet(
	chisel.NewService,
	kubernetes.New,
	kubernetes.NewDiscoveryClient,
	kubernetes.NewResourceRepo,
	otterscale.NewFleetRegistrar,
)
