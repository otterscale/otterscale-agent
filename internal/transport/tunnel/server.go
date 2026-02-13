package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/google/uuid"
	chserver "github.com/jpillora/chisel/server"
)

// ServerOption configures a Server.
type ServerOption func(*Server)

// Server manages a chisel reverse-tunnel listener with TLS fingerprint
// authentication and automatic user provisioning.
type Server struct {
	inner   *chserver.Server // shared pointer; see WithServer
	address string
	keySeed string
	log     *slog.Logger
}

// WithAddress configures the listen address (e.g. ":8300").
func WithAddress(address string) ServerOption {
	return func(s *Server) { s.address = address }
}

// WithKeySeed configures the deterministic key seed for the tunnel server.
func WithKeySeed(keySeed string) ServerOption {
	return func(s *Server) { s.keySeed = keySeed }
}

// WithServer injects a shared chisel server pointer. The pointer is
// typically owned by a TunnelProvider; Start will initialize it in place
// so that both sides share the same running server instance.
func WithServer(srv *chserver.Server) ServerOption {
	return func(s *Server) { s.inner = srv }
}

// WithServerLogger configures a structured logger. Defaults to
// slog.Default with a "component" attribute.
func WithServerLogger(log *slog.Logger) ServerOption {
	return func(s *Server) { s.log = log }
}

// NewServer creates a tunnel server. The underlying chisel server is
// fully initialized so that AddUser (via TunnelProvider) works
// immediately, even before Start is called.
func NewServer(opts ...ServerOption) (*Server, error) {
	s := &Server{
		inner:   &chserver.Server{},
		address: ":8300",
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = slog.Default().With("component", "tunnel-server")
	}
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("tunnel server init: %w", err)
	}
	return s, nil
}

// Start begins accepting connections and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	host, port, err := net.SplitHostPort(s.address)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", s.address, err)
	}

	s.log.Info("starting", "address", s.address)

	if err := s.inner.StartContext(ctx, host, port); err != nil {
		return fmt.Errorf("tunnel server start: %w", err)
	}

	return s.inner.Wait()
}

// Stop gracefully shuts down the tunnel server.
func (s *Server) Stop(_ context.Context) error {
	if s.inner == nil {
		return nil
	}
	s.log.Info("shutting down")
	return s.inner.Close()
}

// init creates the real chisel server and copies it into the shared
// pointer so that any TunnelProvider holding the same reference sees
// the fully initialized instance.
func (s *Server) init() error {
	ch, err := chserver.NewServer(&chserver.Config{
		Reverse: true,
		KeySeed: s.keySeed,
	})
	if err != nil {
		return err
	}

	// Chisel allows anonymous connections when no users exist.
	// Add a disabled sentinel user to enforce authentication.
	if err := ch.AddUser(uuid.NewString(), uuid.NewString(), "127.0.0.1"); err != nil {
		return err
	}

	// Copy into the shared pointer so the TunnelProvider sees the
	// initialized server.
	*s.inner = *ch
	return nil
}
