// Package main is the entry point for the otterscale binary. It
// supports two subcommands:
//
//   - server: runs the control-plane (gRPC API + tunnel listener)
//   - agent:  runs inside a Kubernetes cluster and reverse-proxies
//     API requests through the tunnel
//
// Dependencies are assembled via Google Wire; see wire.go.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/cmd/agent"
	"github.com/otterscale/otterscale-agent/internal/cmd/server"
	"github.com/otterscale/otterscale-agent/internal/config"
)

// version is injected at build time via -ldflags
// (e.g. -ldflags "-X main.version=v1.2.3").
var version = "devel"

func main() {
	// Cancel on SIGINT (Ctrl+C) or SIGTERM (container runtime).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// Cobra is configured with SilenceErrors: true, so we
		// print the error here for consistent formatting.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// run wires all dependencies and executes the root Cobra command.
func run(ctx context.Context) error {
	rootCmd, cleanup, err := wireCmd()
	if err != nil {
		return fmt.Errorf("failed to initialize application: %w", err)
	}
	defer cleanup()

	return rootCmd.ExecuteContext(ctx)
}

// newCmd is a Wire provider that constructs the root Cobra command and
// registers the server and agent subcommands.
func newCmd(conf *config.Config) (*cobra.Command, error) {
	c := &cobra.Command{
		Use:           "otterscale",
		Short:         "OtterScale: A unified platform for simplified compute, storage, and networking.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	serverCmd, err := cmd.NewServerCommand(conf, func() (*server.Server, func(), error) {
		return wireServer()
	})
	if err != nil {
		return nil, err
	}

	agentCmd, err := cmd.NewAgentCommand(conf, func() (*agent.Agent, func(), error) {
		return wireAgent()
	})
	if err != nil {
		return nil, err
	}

	c.AddCommand(serverCmd, agentCmd)

	return c, nil
}
