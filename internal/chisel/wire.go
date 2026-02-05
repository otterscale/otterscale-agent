package chisel

import "github.com/google/wire"

var ProviderSet = wire.NewSet(NewTunnelService)
