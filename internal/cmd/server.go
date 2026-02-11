package cmd

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/otterscale/otterscale-agent/internal/mux/impersonation"
	"github.com/spf13/cobra"
)

// ServerDeps holds the dependencies that are only needed by the server subcommand.
// These are created lazily (via ServerDepsFactory) so that running `otterscale agent`
// does not trigger their initialization (e.g. chisel tunnel server).
type ServerDeps struct {
	Hub    *mux.Hub
	Tunnel core.TunnelProvider
}

// NewServerDeps is a Wire provider that assembles the ServerDeps struct.
func NewServerDeps(hub *mux.Hub, tunnel core.TunnelProvider) *ServerDeps {
	return &ServerDeps{Hub: hub, Tunnel: tunnel}
}

// ServerDepsFactory creates server-specific dependencies on demand.
type ServerDepsFactory func() (*ServerDeps, func(), error)

// TODO: replicated server
func NewServer(conf *config.Config, factory ServerDepsFactory, interceptors ...connect.Interceptor) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --tunnel-address=127.0.0.1:8300",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Initialize server-specific dependencies only when the server
			// subcommand actually runs, avoiding eager chisel initialization.
			deps, cleanup, err := factory()
			if err != nil {
				return fmt.Errorf("failed to initialize server dependencies: %w", err)
			}
			defer cleanup()

			var (
				address        = conf.ServerAddress()
				allowedOrigins = conf.ServerAllowedOrigins()
				tunnelAddress  = conf.ServerTunnelAddress()
			)

			slog.Info("Starting tunnel server", "address", tunnelAddress)
			if err := deps.Tunnel.Start(tunnelAddress); err != nil {
				return fmt.Errorf("failed to start tunnel server: %w", err)
			}

			if !conf.ServerDebugEnabled() {
				openTelemetryInterceptor, err := otelconnect.NewInterceptor()
				if err != nil {
					return err
				}

				impersonationInterceptor, err := impersonation.NewInterceptor(conf)
				if err != nil {
					return err
				}

				interceptors = append(interceptors, openTelemetryInterceptor, impersonationInterceptor)
			}

			slog.Info("Starting HTTP server", "address", address, "allowedOrigins", allowedOrigins)
			return startHTTPServer(cmd.Context(), deps.Hub, address, allowedOrigins, connect.WithInterceptors(interceptors...))
		},
	}

	if err := conf.BindFlags(cmd.Flags(), config.ServerOptions); err != nil {
		return nil, err
	}

	return cmd, nil
}
