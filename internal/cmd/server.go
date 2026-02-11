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
func NewServer(conf *config.Config, hub *mux.Hub, tunnel core.TunnelProvider) *cobra.Command {
	var (
		address, tunnelAddress string
	)

	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --config=otterscale.yaml",
		RunE: func(_ *cobra.Command, _ []string) error {
			slog.Info("Starting tunnel server", "address", tunnelAddress)
			if err := tunnel.Start(tunnelAddress); err != nil {
				return fmt.Errorf("failed to start tunnel server: %w", err)
			}

			openTelemetryInterceptor, err := otelconnect.NewInterceptor()
			if err != nil {
				return err
			}

			impersonationInterceptor, err := impersonation.NewInterceptor(conf)
			if err != nil {
				return err
			}

			slog.Info("Starting HTTP server", "address", address)
			return startHTTPServer(address, hub, connect.WithInterceptors(openTelemetryInterceptor, impersonationInterceptor))
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
