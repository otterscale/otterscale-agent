package server

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	fleetv1 "github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/middleware"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

type Config struct {
	Address          string
	AllowedOrigins   []string
	TunnelAddress    string
	TunnelKeySeed    string
	KeycloakRealmURL string
	KeycloakClientID string
}

type Server struct {
	handler *Handler
	tunnel  core.TunnelProvider
}

func NewServer(handler *Handler, tunnel core.TunnelProvider) *Server {
	return &Server{handler: handler, tunnel: tunnel}
}

func (s *Server) Run(ctx context.Context, cfg Config) error {
	oidc, err := middleware.NewOIDC(cfg.KeycloakRealmURL, cfg.KeycloakClientID)
	if err != nil {
		return fmt.Errorf("failed to create OIDC middleware: %w", err)
	}

	httpSrv, err := http.NewServer(
		http.WithAddress(cfg.Address),
		http.WithAllowedOrigins(cfg.AllowedOrigins),
		http.WithAuthMiddleware(oidc),
		http.WithPublicPaths([]string{
			fleetv1.FleetServiceRegisterProcedure,
		}),
		http.WithMount(s.handler.Mount),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP server: %w", err)
	}

	tunnelSrv, err := tunnel.NewServer(
		tunnel.WithAddress(cfg.TunnelAddress),
		tunnel.WithKeySeed(cfg.TunnelKeySeed),
		tunnel.WithServer(s.tunnel.Server),
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
