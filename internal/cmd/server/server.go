// Package server implements the control-plane runtime that serves the
// public gRPC/HTTP API and manages the chisel tunnel listener.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	fleetv1 "github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/middleware"
	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
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

// Server binds an HTTP server (gRPC + REST) and a chisel tunnel
// listener, running them in parallel via transport.Serve.
type Server struct {
	handler *Handler
	tunnel  *chisel.Service
	runtime *core.RuntimeUseCase
}

// NewServer returns a Server wired to the given handler and tunnel
// provider. It accepts the concrete *chisel.Service rather than the
// core.TunnelProvider interface because it needs direct access to the
// underlying chisel server and CA for transport initialisation.
func NewServer(handler *Handler, tunnel *chisel.Service, runtime *core.RuntimeUseCase) *Server {
	return &Server{handler: handler, tunnel: tunnel, runtime: runtime}
}

// Run starts both the HTTP and tunnel servers. It blocks until ctx
// is cancelled or an unrecoverable error occurs. Health, reflection,
// and fleet-registration endpoints are marked as public (no auth).
func (s *Server) Run(ctx context.Context, cfg Config) error {
	if cfg.KeycloakRealmURL == "" {
		return fmt.Errorf("keycloak realm URL is required but not configured")
	}

	ca := s.tunnel.CA()

	// Generate a server TLS certificate signed by the CA. Parse the
	// tunnel address to extract the host for the certificate SAN.
	tunnelHost, _, err := net.SplitHostPort(cfg.TunnelAddress)
	if err != nil {
		return fmt.Errorf("parse tunnel address %q: %w", cfg.TunnelAddress, err)
	}
	serverCert, serverKey, err := ca.GenerateServerCert(tunnelHost)
	if err != nil {
		return fmt.Errorf("failed to generate server cert: %w", err)
	}

	// Write CA cert, server cert, and server key to a temp directory
	// so chisel can load them via file paths.
	certDir, err := os.MkdirTemp("", "otterscale-tls-server-*")
	if err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}
	defer os.RemoveAll(certDir)

	caFile := filepath.Join(certDir, "ca.pem")
	certFile := filepath.Join(certDir, "cert.pem")
	keyFile := filepath.Join(certDir, "key.pem")

	if err := os.WriteFile(caFile, ca.CertPEM(), 0600); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(certFile, serverCert, 0600); err != nil {
		return fmt.Errorf("write server cert: %w", err)
	}
	if err := os.WriteFile(keyFile, serverKey, 0600); err != nil {
		return fmt.Errorf("write server key: %w", err)
	}

	slog.Info("tunnel CA initialized", "subject", "otterscale-ca")

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
		tunnel.WithTLSCert(certFile),
		tunnel.WithTLSKey(keyFile),
		tunnel.WithTLSCA(caFile),
		tunnel.WithServer(s.tunnel.ServerRef()),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel server: %w", err)
	}

	// Detect disconnected tunnel clients and remove stale registrations.
	// The health checker runs as a managed listener so that panics are
	// caught and shutdown is coordinated with the other servers.
	healthChecker := chisel.NewHealthCheckListener(s.tunnel)

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
