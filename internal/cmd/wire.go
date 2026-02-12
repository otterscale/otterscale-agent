package cmd

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/cmd/server"
)

var ProviderSet = wire.NewSet(
	server.NewServer,
	server.NewHandler,
)
