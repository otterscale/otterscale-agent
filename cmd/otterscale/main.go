package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/chisel"
	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/leader"
	"github.com/otterscale/otterscale-agent/internal/mux"
)

var version = "devel"

func newCmd(conf *config.Config, hub *mux.Hub, spoke *mux.Spoke, tunnels *chisel.TunnelService, elector *leader.Elector) *cobra.Command {
	c := &cobra.Command{
		Use:           "otterscale",
		Short:         "OtterScale: A unified platform for simplified compute, storage, and networking.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(
		cmd.NewServer(conf, hub, tunnels, elector),
		cmd.NewAgent(conf, spoke),
	)
	return c
}

func run() error {
	// options
	grpcHelper := true

	// wire cmd
	cmd, cleanup, err := wireCmd(grpcHelper)
	if err != nil {
		return err
	}
	defer cleanup()

	// start and wait for stop signal
	return cmd.Execute()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
