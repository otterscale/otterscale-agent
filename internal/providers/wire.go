package providers

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/providers/kubernetes"
)

var ProviderSet = wire.NewSet(
	kubernetes.New,
	kubernetes.NewDiscoveryClient,
	kubernetes.NewResourceRepo,
)
