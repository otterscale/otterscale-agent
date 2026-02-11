package providers

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/providers/kubernetes"
)

var ProviderSet = wire.NewSet(
	chisel.NewChiselService,
	kubernetes.New,
	kubernetes.NewDiscoveryClient,
	kubernetes.NewResourceRepo,
)
