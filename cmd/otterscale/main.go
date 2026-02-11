package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/config"
)

// version is typically injected at build time via -ldflags.
var version = "devel"

func main() {
	// The context will be canceled when SIGINT (Ctrl+C) or SIGTERM is received.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// Handle final error output and exit code here.
		// Since Cobra is set to SilenceErrors: true, we must print the error explicitly.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// wireCmd assembles all dependencies and returns the root command and a cleanup function.
	rootCmd, cleanup, err := wireCmd()
	if err != nil {
		return fmt.Errorf("failed to initialize application: %w", err)
	}
	// Ensure cleanup (closing DB connections, file handles, etc.) is executed when run returns.
	defer cleanup()

	// ExecuteContext listens to ctx.Done() and propagates the cancellation signal to subcommands.
	return rootCmd.ExecuteContext(ctx)
}

// newCmd is a Wire provider responsible for constructing the Root Command.
// Server-specific dependencies (Hub, TunnelProvider) are created lazily via
// wireServerDeps so that running `otterscale agent` does not trigger their
// initialization (e.g. chisel tunnel server).
func newCmd(conf *config.Config) (*cobra.Command, error) {
	c := &cobra.Command{
		Use:           "otterscale",
		Short:         "OtterScale: A unified platform for simplified compute, storage, and networking.",
		Version:       version,
		SilenceUsage:  true, // Do not show usage on error, unless it is a flag/argument error.
		SilenceErrors: true, // Silence errors here so we can handle printing centrally in main.
	}

	serverCmd, err := cmd.NewServer(conf, func() (*cmd.ServerDeps, func(), error) {
		return wireServerDeps(conf)
	})
	if err != nil {
		return nil, err
	}

	agentCmd, err := cmd.NewAgent(conf)
	if err != nil {
		return nil, err
	}

	c.AddCommand(serverCmd, agentCmd)

	return c, nil
}
