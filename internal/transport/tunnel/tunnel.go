package tunnel

import (
	"context"
	"log/slog"
	"net"

	chserver "github.com/jpillora/chisel/server"
)

// Option defines a functional option for configuring the tunnel.
type ServerOption func(*Server)

type Server struct {
	*chserver.Server
	address string
	keySeed string
}

// WithAddress configures the tunnel address.
func WithAddress(address string) ServerOption {
	return func(o *Server) {
		o.address = address
	}
}

// WithKeySeed configures the tunnel key seed.
func WithKeySeed(keySeed string) ServerOption {
	return func(o *Server) {
		o.keySeed = keySeed
	}
}

// NewServer creates a new tunnel server with the given options.
func NewServer(opts ...ServerOption) (*Server, error) {
	srv := &Server{
		address: ":8300",
	}
	for _, opt := range opts {
		opt(srv)
	}

	cfg := &chserver.Config{
		Reverse: true,
		KeySeed: srv.keySeed,
	}

	chServer, err := chserver.NewServer(cfg)
	if err != nil {
		return nil, err
	}

	srv.Server = chServer
	return srv, nil
}

// Start starts the tunnel server and blocks until the context is canceled.
func (s *Server) Start(ctx context.Context) error {
	host, port, err := net.SplitHostPort(s.address)
	if err != nil {
		return err
	}

	slog.Info("Tunnel starting on", "address", s.address)

	if err := s.Server.StartContext(ctx, host, port); err != nil {
		return err
	}

	return s.Server.Wait()
}

// Stop stops the tunnel server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("Gracefully shutting down tunnel server...")
	return s.Server.Close()
}
