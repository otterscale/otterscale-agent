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
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

// provideCA is a Wire provider that loads the CA from the configured
// directory. On first startup the directory is empty, so a new CA is
// generated (using crypto/rand backed by a FIPS-approved DRBG) and
// persisted. Subsequent restarts load the existing CA, keeping
// previously issued agent certificates valid.
func provideCA(conf *config.Config) (*pki.CA, error) {
	dir := conf.ServerTunnelCADir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	certPEM, errC := os.ReadFile(certPath)
	keyPEM, errK := os.ReadFile(keyPath)
	if errC == nil && errK == nil {
		slog.Info("loading existing CA", "dir", dir)
		return pki.LoadCA(certPEM, keyPEM)
	}

	// First run: generate and persist.
	slog.Info("generating new CA", "dir", dir)
	ca, err := pki.NewCA()
	if err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create CA dir: %w", err)
	}

	keyPEM, err = ca.KeyPEM()
	if err != nil {
		return nil, fmt.Errorf("export CA key: %w", err)
	}

	if err := os.WriteFile(certPath, ca.CertPEM(), 0600); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("write CA key: %w", err)
	}

	return ca, nil
}

// provideInClusterConfig is a Wire provider that returns a
// *rest.Config for in-cluster Kubernetes API access. It falls back to
// the user's kubeconfig for local development.
func provideInClusterConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Warn("in-cluster config not available, falling back to kubeconfig", "error", err)
		return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	}
	return cfg, nil
}

// provideAgentManifestConfig is a Wire provider that extracts the
// external URLs from the server configuration and derives an HMAC key
// for signing stateless manifest tokens. The HMAC key is derived from
// the CA's private key via HKDF, so it is deterministic for the same
// CA and survives restarts without separate persistence.
func provideAgentManifestConfig(conf *config.Config, ca *pki.CA) (core.AgentManifestConfig, error) {
	hmacKey, err := ca.DeriveHMACKey("manifest-token")
	if err != nil {
		return core.AgentManifestConfig{}, fmt.Errorf("derive HMAC key: %w", err)
	}
	return core.AgentManifestConfig{
		ServerURL: conf.ServerExternalURL(),
		TunnelURL: conf.ServerExternalTunnelURL(),
		HMACKey:   hmacKey,
	}, nil
}
