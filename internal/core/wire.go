package core

import (
	"github.com/google/wire"
)

// provideDiscoveryCache constructs a DiscoveryCache with the default TTL.
// This is a Wire provider that bridges the DiscoveryClient to the
// SchemaResolver interface via caching.
func provideDiscoveryCache(discovery DiscoveryClient) *DiscoveryCache {
	return NewDiscoveryCache(discovery, DiscoveryCacheTTL)
}

// ProviderSet is the Wire provider set for all domain use-cases.
var ProviderSet = wire.NewSet(
	NewFleetUseCase,
	NewResourceUseCase,
	NewRuntimeUseCase,
	provideDiscoveryCache,
	wire.Bind(new(SchemaResolver), new(*DiscoveryCache)),
)
