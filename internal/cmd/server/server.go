// Package server implements the control-plane runtime that serves the
// public gRPC/HTTP API and manages the chisel tunnel listener.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	fleetv1 "github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/middleware"
	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

// defaultKeySeed is the insecure placeholder that ships in config
// defaults. The server refuses to start if it is still in use.
const defaultKeySeed = "change-me"

// Config holds the runtime parameters for a Server.
type Config struct {
	Address          string
	AllowedOrigins   []string
	TunnelAddress    string
	TunnelKeySeed    string
	KeycloakRealmURL string
	KeycloakClientID string
}

// Server binds an HTTP server (gRPC + REST) and a chisel tunnel
// listener, running them in parallel via transport.Serve.
type Server struct {
	handler *Handler
	tunnel  *chisel.Service
}

// NewServer returns a Server wired to the given handler and tunnel
// provider. It accepts the concrete *chisel.Service rather than the
// core.TunnelProvider interface because it needs direct access to the
// underlying chisel server for transport initialisation.
func NewServer(handler *Handler, tunnel *chisel.Service) *Server {
	return &Server{handler: handler, tunnel: tunnel}
}

// Run starts both the HTTP and tunnel servers. It blocks until ctx
// is cancelled or an unrecoverable error occurs. Health, reflection,
// and fleet-registration endpoints are marked as public (no auth).
func (s *Server) Run(ctx context.Context, cfg Config) error {
	if cfg.TunnelKeySeed == defaultKeySeed {
		return errors.New("refusing to start: tunnel key seed is the insecure default \"change-me\"; " +
			"set --tunnel-key-seed or OTTERSCALE_SERVER_TUNNEL_KEY_SEED to a unique secret")
	}

	// Warn about unauthenticated fleet registration endpoint.
	slog.Warn("fleet Register endpoint is publicly accessible without authentication; " +
		"consider adding a pre-shared token or mTLS for agent registration in production")

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
		tunnel.WithServer(s.tunnel.Server()),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel server: %w", err)
	}

	return transport.Serve(ctx, httpSrv, tunnelSrv)
}
