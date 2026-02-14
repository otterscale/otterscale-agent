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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/cmd/agent"
	"github.com/otterscale/otterscale-agent/internal/cmd/server"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/pki"
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
// registers the server and agent subcommands. The version is captured
// by closures passed to the Wire injectors so that the Injector type
// signatures remain unchanged.
func newCmd(conf *config.Config) (*cobra.Command, error) {
	c := &cobra.Command{
		Use:           "otterscale",
		Short:         "OtterScale: A unified platform for simplified compute, storage, and networking.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	v := core.Version(version)

	serverCmd, err := cmd.NewServerCommand(conf, func() (*server.Server, func(), error) {
		return wireServer(v, conf)
	})
	if err != nil {
		return nil, err
	}

	agentCmd, err := cmd.NewAgentCommand(conf, func() (*agent.Agent, func(), error) {
		return wireAgent(v)
	})
	if err != nil {
		return nil, err
	}

	c.AddCommand(serverCmd, agentCmd)

	return c, nil
}

// provideCA is a Wire provider that creates a deterministic CA from
// the configured seed. It validates that the seed is not the insecure
// default, failing fast at dependency injection time rather than at
// runtime.
func provideCA(conf *config.Config) (*pki.CA, error) {
	seed := conf.ServerTunnelCASeed()
	if seed == config.InsecureDefaultCASeed {
		return nil, fmt.Errorf("refusing to start: tunnel CA seed is the insecure default %q; "+
			"set --tunnel-ca-seed or OTTERSCALE_SERVER_TUNNEL_CA_SEED to a unique secret", config.InsecureDefaultCASeed)
	}
	return pki.NewCAFromSeed(seed)
}

// provideInClusterConfig is a Wire provider that returns a
// *rest.Config for in-cluster Kubernetes API access. It falls back to
// the user's kubeconfig for local development.
func provideInClusterConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	}
	return cfg, nil
}

// provideAgentManifestConfig is a Wire provider that extracts the
// external URLs from the server configuration and derives an HMAC key
// for signing stateless manifest tokens.
func provideAgentManifestConfig(conf *config.Config) core.AgentManifestConfig {
	return core.AgentManifestConfig{
		ServerURL: conf.ServerExternalURL(),
		TunnelURL: conf.ServerExternalTunnelURL(),
		HMACKey:   pki.DeriveHMACKey(conf.ServerTunnelCASeed(), "manifest-token"),
	}
}
