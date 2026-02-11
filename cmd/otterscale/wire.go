//go:build wireinject

package main

import (
	"github.com/google/wire"
	"github.com/otterscale/otterscale-agent/internal/app"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/otterscale/otterscale-agent/internal/providers"
	"github.com/spf13/cobra"
)

func wireCmd() (*cobra.Command, func(), error) {
	panic(wire.Build(newCmd, mux.ProviderSet, app.ProviderSet, core.ProviderSet, providers.ProviderSet, config.ProviderSet))
}
