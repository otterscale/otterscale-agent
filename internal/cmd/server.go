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

// ServerOption configures optional behaviour for the server command.
type ServerOption func(*serverConfig)

type serverConfig struct {
	interceptors []connect.Interceptor
}

// WithInterceptors overrides the default interceptors (OpenTelemetry + OIDC
// impersonation). This is primarily useful for integration tests that need to
// replace the Keycloak OIDC interceptor with a test-only variant.
func WithInterceptors(interceptors ...connect.Interceptor) ServerOption {
	return func(c *serverConfig) {
		c.interceptors = interceptors
	}
}

// TODO: replicated server
func NewServer(conf *config.Config, hub *mux.Hub, tunnel core.TunnelProvider, opts ...ServerOption) *cobra.Command {
	var address, tunnelAddress string

	var sc serverConfig
	for _, o := range opts {
		o(&sc)
	}

	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --tunnel-address=127.0.0.1:16598",
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.Info("Starting tunnel server", "address", tunnelAddress)
			if err := tunnel.Start(tunnelAddress); err != nil {
				return fmt.Errorf("failed to start tunnel server: %w", err)
			}

			interceptors := sc.interceptors
			if interceptors == nil {
				openTelemetryInterceptor, err := otelconnect.NewInterceptor()
				if err != nil {
					return err
				}

				impersonationInterceptor, err := impersonation.NewInterceptor(conf)
				if err != nil {
					return err
				}

				interceptors = []connect.Interceptor{openTelemetryInterceptor, impersonationInterceptor}
			}

			slog.Info("Starting HTTP server", "address", address)
			return startHTTPServer(cmd.Context(), address, hub, conf.CORSAllowedOrigins(), connect.WithInterceptors(interceptors...))
		},
	}

	cmd.Flags().StringVar(
		&address,
		"address",
		":8299",
		"Address for server to listen on (e.g. :8299)",
	)

	cmd.Flags().StringVar(
		&tunnelAddress,
		"tunnel-address",
		"127.0.0.1:16598",
		"Address for tunnel server to listen on (e.g. 127.0.0.1:16598)",
	)

	return cmd
}
