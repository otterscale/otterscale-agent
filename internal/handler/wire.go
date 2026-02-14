package handler

import (
	"github.com/google/wire"
)

// ProviderSet is the Wire provider set for ConnectRPC service handlers.
var ProviderSet = wire.NewSet(NewFleetService, NewResourceService, NewRuntimeService)
