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

// wireCmd assembles the root Cobra command with configuration loaded.
func wireCmd() (*cobra.Command, func(), error) {
	panic(wire.Build(newCmd, config.ProviderSet))
}

// wireServer assembles a fully wired Server with all gRPC services,
// use-cases, and infrastructure providers. The version parameter is
// provided by the caller and flows through Wire to FleetUseCase.
// The config parameter provides the CA seed for mTLS certificate
// issuance via provideCA.
func wireServer(v core.Version, conf *config.Config) (*server.Server, func(), error) {
	panic(wire.Build(cmd.ProviderSet, app.ProviderSet, core.ProviderSet, providers.ProviderSet, provideCA))
}

// wireAgent assembles a fully wired Agent with its handler and fleet
// registrar. The version parameter is provided by the caller and flows
// through Wire to both FleetRegistrar and Agent.
func wireAgent(v core.Version) (*agent.Agent, func(), error) {
	panic(wire.Build(cmd.ProviderSet, providers.ProviderSet))
}
