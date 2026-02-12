package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/cmd/server"
	"github.com/otterscale/otterscale-agent/internal/config"
)

type ServerInjector func() (*server.Server, func(), error)

// TODO: replicated server
func NewServerCommand(conf *config.Config, newServer ServerInjector) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --tunnel-address=127.0.0.1:8300",
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, cleanup, err := newServer()
			if err != nil {
				return fmt.Errorf("failed to initialize server: %w", err)
			}
			defer cleanup()

			cfg := server.Config{
				Address:          conf.ServerAddress(),
				AllowedOrigins:   conf.ServerAllowedOrigins(),
				TunnelAddress:    conf.ServerTunnelAddress(),
				TunnelKeySeed:    conf.ServerTunnelKeySeed(),
				KeycloakRealmURL: conf.ServerKeycloakRealmURL(),
				KeycloakClientID: conf.ServerKeycloakClientID(),
			}

			return srv.Run(cmd.Context(), cfg)
		},
	}

	if err := conf.BindFlags(cmd.Flags(), config.ServerOptions); err != nil {
		return nil, err
	}

	return cmd, nil
}
