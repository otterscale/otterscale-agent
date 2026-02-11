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

// TODO: replicated server
func NewServer(conf *config.Config, hub *mux.Hub, tunnel core.TunnelProvider, interceptors ...connect.Interceptor) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --tunnel-address=127.0.0.1:16598",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var (
				address        = conf.ServerAddress()
				allowedOrigins = conf.ServerAllowedOrigins()
				tunnelAddress  = conf.ServerTunnelAddress()
			)

			slog.Info("Starting tunnel server", "address", tunnelAddress)
			if err := tunnel.Start(tunnelAddress); err != nil {
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
			return startHTTPServer(cmd.Context(), hub, address, allowedOrigins, connect.WithInterceptors(interceptors...))
		},
	}

	if err := conf.BindFlags(cmd.Flags(), config.ServerOptions); err != nil {
		return nil, err
	}

	return cmd, nil
}
