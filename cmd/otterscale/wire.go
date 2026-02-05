//go:build wireinject

package main

import (
	"github.com/google/wire"
	"github.com/otterscale/otterscale-agent/internal/app"
	"github.com/otterscale/otterscale-agent/internal/chisel"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/kubernetes"
	"github.com/otterscale/otterscale-agent/internal/leader"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/spf13/cobra"
)

func wireCmd(bool) (*cobra.Command, func(), error) {
	panic(wire.Build(
		newCmd,
		wire.Bind(new(mux.HubResourceHandler), new(*app.ResourceProxy)),
		wire.Bind(new(mux.SpokeResourceHandler), new(*app.ResourceService)),
		mux.ProviderSet,
		app.ProviderSet,
		core.ProviderSet,
		kubernetes.ProviderSet,
		chisel.ProviderSet,
		leader.ProviderSet,
		config.ProviderSet,
	))
}
