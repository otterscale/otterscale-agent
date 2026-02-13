package server

import (
	"context"
	"fmt"

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
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
			"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
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

	return transport.Serve(ctx, httpSrv, tunnelSrv)
}
