// Package server implements the control-plane runtime that serves the
// public gRPC/HTTP API and manages the chisel tunnel listener.
package server

import (
	"context"
	"fmt"
	"net"
	"time"

	fleetv1 "github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/middleware"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
)

// Config holds the runtime parameters for a Server.
type Config struct {
	Address          string
	AllowedOrigins   []string
	TunnelAddress    string
	KeycloakRealmURL string
	KeycloakClientID string
}

// sessionReapInterval is the interval at which the session reaper
// scans for and removes stale sessions.
const sessionReapInterval = 30 * time.Second

// TunnelService provides the tunnel infrastructure needed by the
// server for transport setup and health monitoring. The interface is
// defined at the consumer side (server package) following Go
// conventions, decoupling the server from concrete implementations
// such as chisel.Service.
type TunnelService interface {
	// BuildTunnelListener creates a fully configured tunnel server
	// listener for the given address and host SAN.
	BuildTunnelListener(address, host string) (transport.Listener, error)
	// BuildHealthListener returns a transport.Listener that performs
	// periodic health checks on registered tunnel endpoints.
	BuildHealthListener() transport.Listener
}

// Server binds an HTTP server (gRPC + REST) and a chisel tunnel
// listener, running them in parallel via transport.Serve.
type Server struct {
	handler *Handler
	tunnel  TunnelService
	runtime *core.RuntimeUseCase
}

// NewServer returns a Server wired to the given handler and tunnel
// service. The TunnelService interface decouples the server from
// concrete tunnel implementations, keeping infrastructure details
// behind the interface boundary.
func NewServer(handler *Handler, tunnel TunnelService, runtime *core.RuntimeUseCase) *Server {
	return &Server{handler: handler, tunnel: tunnel, runtime: runtime}
}

// Run starts both the HTTP and tunnel servers. It blocks until ctx
// is cancelled or an unrecoverable error occurs. Health, reflection,
// and fleet-registration endpoints are marked as public (no auth).
func (s *Server) Run(ctx context.Context, cfg Config) error {
	if cfg.KeycloakRealmURL == "" {
		return fmt.Errorf("keycloak realm URL is required but not configured")
	}

	// Parse the tunnel address to extract the host for the TLS
	// certificate SAN.
	tunnelHost, _, err := net.SplitHostPort(cfg.TunnelAddress)
	if err != nil {
		return fmt.Errorf("parse tunnel address %q: %w", cfg.TunnelAddress, err)
	}

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
		http.WithPublicPathPrefixes([]string{
			"/fleet/manifest/",
		}),
		http.WithMount(s.handler.Mount),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP server: %w", err)
	}

	// Build the tunnel server listener with mTLS via the injected
	// TunnelService. Certificate generation and file I/O are
	// encapsulated behind the interface.
	tunnelSrv, err := s.tunnel.BuildTunnelListener(cfg.TunnelAddress, tunnelHost)
	if err != nil {
		return fmt.Errorf("failed to create tunnel server: %w", err)
	}

	// Detect disconnected tunnel clients and remove stale
	// registrations.
	healthChecker := s.tunnel.BuildHealthListener()

	// Session reaper periodically cleans up finished but unreleased
	// exec/port-forward sessions to prevent memory leaks.
	reaper := &sessionReaperListener{runtime: s.runtime}

	return transport.Serve(ctx, httpSrv, tunnelSrv, healthChecker, reaper)
}

// sessionReaperListener adapts RuntimeUseCase.StartSessionReaper to
// the transport.Listener interface so it participates in the managed
// lifecycle alongside other servers.
type sessionReaperListener struct {
	runtime *core.RuntimeUseCase
}

func (l *sessionReaperListener) Start(ctx context.Context) error {
	l.runtime.StartSessionReaper(ctx, sessionReapInterval)
	return nil
}

func (l *sessionReaperListener) Stop(_ context.Context) error {
	return nil // reaper stops when its context is cancelled
}
