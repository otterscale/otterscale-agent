package tunnel

import (
	"context"
	"log/slog"
	"net"

	"github.com/google/uuid"
	chserver "github.com/jpillora/chisel/server"
)

// ServerFunc is a function that returns a existing chisel server.
type ServerFunc func() *chserver.Server

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

// WithServer configures the tunnel server.
func WithServer(server ServerFunc) ServerOption {
	return func(o *Server) {
		o.Server = server()
	}
}

// NewServer creates a new tunnel server with the given options.
func NewServer(opts ...ServerOption) (*Server, error) {
	srv := &Server{
		address: ":8300",
		Server:  &chserver.Server{},
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

	// if no users exist, chisel will allow anyone to connect.
	disabledUser, disabledPass := uuid.NewString(), uuid.NewString()
	if err := chServer.AddUser(disabledUser, disabledPass, "127.0.0.1"); err != nil {
		return nil, err
	}

	*srv.Server = *chServer // replace the lazy initialized server
	return srv, nil
}

// Start starts the tunnel server and blocks until the context is canceled.
func (s *Server) Start(ctx context.Context) error {
	host, port, err := net.SplitHostPort(s.address)
	if err != nil {
		return err
	}

	slog.Info("Tunnel server starting on", "address", s.address)

	if err := s.StartContext(ctx, host, port); err != nil {
		return err
	}

	return s.Wait()
}

// Stop stops the tunnel server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("Gracefully shutting down tunnel server...")
	return s.Close()
}
