//go:build wireinject

package main

import (
	"github.com/google/wire"
	"github.com/otterscale/otterscale-agent/internal/app"
	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/otterscale/otterscale-agent/internal/providers"
	"github.com/spf13/cobra"
)

// wireCmd builds the root command with only lightweight dependencies (Config).
// Server-specific dependencies are deferred to wireServerDeps.
func wireCmd() (*cobra.Command, func(), error) {
	panic(wire.Build(newCmd, config.ProviderSet))
}

// wireServerDeps builds the heavy server-specific dependencies (Hub, TunnelProvider)
// on demand. This is called lazily inside the server subcommand's RunE, so that
// running `otterscale agent` never triggers chisel server initialization.
func wireServerDeps(conf *config.Config) (*cmd.ServerDeps, func(), error) {
	panic(wire.Build(cmd.NewServerDeps, mux.ProviderSet, app.ProviderSet, core.ProviderSet, providers.ProviderSet))
}
