//go:build wireinject

package main

import (
	"github.com/google/wire"
	"github.com/otterscale/otterscale-agent/internal/app"
	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/cmd/agent"
	"github.com/otterscale/otterscale-agent/internal/cmd/server"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/providers"
	"github.com/spf13/cobra"
)

func wireCmd() (*cobra.Command, func(), error) {
	panic(wire.Build(newCmd, config.ProviderSet))
}

func wireServer() (*server.Server, func(), error) {
	panic(wire.Build(cmd.ProviderSet, app.ProviderSet, core.ProviderSet, providers.ProviderSet))
}

func wireAgent() (*agent.Agent, func(), error) {
	panic(wire.Build(cmd.ProviderSet, providers.ProviderSet))
}
