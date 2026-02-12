package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/middleware"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

type ServerInjector func() (*Server, func(), error)

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

			cfg := serverConfig{
				address:          conf.ServerAddress(),
				allowedOrigins:   conf.ServerAllowedOrigins(),
				tunnelAddress:    conf.ServerTunnelAddress(),
				tunnelKeySeed:    conf.ServerTunnelKeySeed(),
				keycloakRealmURL: conf.ServerKeycloakRealmURL(),
				keycloakClientID: conf.ServerKeycloakClientID(),
			}

			return srv.Run(cmd.Context(), cfg)
		},
	}

	if err := conf.BindFlags(cmd.Flags(), config.ServerOptions); err != nil {
		return nil, err
	}

	return cmd, nil
}

type serverConfig struct {
	address          string
	allowedOrigins   []string
	tunnelAddress    string
	tunnelKeySeed    string
	keycloakRealmURL string
	keycloakClientID string
}

type Server struct {
	hub *mux.Hub
}

func NewServer(hub *mux.Hub) *Server {
	return &Server{hub: hub}
}

func (s *Server) Run(ctx context.Context, cfg serverConfig) error {
	oidc, err := middleware.NewOIDC(cfg.keycloakRealmURL, cfg.keycloakClientID)
	if err != nil {
		return fmt.Errorf("failed to create OIDC middleware: %w", err)
	}

	httpSrv, err := http.NewServer(
		http.WithAddress(cfg.address),
		http.WithMount(s.hub.RegisterHandlers),
		http.WithAuthMiddleware(oidc),
		http.WithAllowedOrigins(cfg.allowedOrigins),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP server: %w", err)
	}

	tunnelSrv, err := tunnel.NewServer(
		tunnel.WithAddress(cfg.tunnelAddress),
		tunnel.WithKeySeed(cfg.tunnelKeySeed),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel server: %w", err)
	}

	srvs := []transport.Server{httpSrv, tunnelSrv}
	eg, ctx := errgroup.WithContext(ctx)

	for _, srv := range srvs {
		eg.Go(func() error {
			return srv.Start(ctx)
		})

		eg.Go(func() error {
			<-ctx.Done()

			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return srv.Stop(stopCtx)
		})
	}

	return eg.Wait()
}
