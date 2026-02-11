package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/mux"
)

var version = "devel"

func newCmd(conf *config.Config, hub *mux.Hub, tunnel core.TunnelProvider) *cobra.Command {
	c := &cobra.Command{
		Use:           "otterscale",
		Short:         "OtterScale: A unified platform for simplified compute, storage, and networking.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(
		cmd.NewServer(conf, hub, tunnel),
		cmd.NewAgent(),
	)
	return c
}

func run() error {
	// wire cmd
	cmd, cleanup, err := wireCmd()
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
