package kubernetes

import (
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	New,
	NewDiscoveryRepo,
	NewResourceRepo,
)
