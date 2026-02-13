package cmd

import (
	"github.com/google/wire"

	"github.com/otterscale/otterscale-agent/internal/cmd/agent"
	"github.com/otterscale/otterscale-agent/internal/cmd/server"
)

var ProviderSet = wire.NewSet(
	agent.NewAgent,
	agent.NewHandler,
	server.NewServer,
	server.NewHandler,
)
