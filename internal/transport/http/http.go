package http

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/authn"
	connectcors "connectrpc.com/cors"
	"github.com/rs/cors"
)

// MountFunc defines a function that registers handlers onto the provided ServeMux.
// By passing *http.ServeMux, we allow the caller to register multiple services.
type MountFunc func(mux *http.ServeMux) error

// Option defines a functional option for configuring the server.
type ServerOption func(*Server)

type Server struct {
	*http.Server
	address        string
	mount          MountFunc
	authMiddleware *authn.Middleware
	publicPaths    map[string]struct{}
	allowedOrigins []string
}

// WithAddress configures the server address.
func WithAddress(address string) ServerOption {
	return func(o *Server) {
		o.address = address
	}
}

// WithMount configures the mount function.
func WithMount(mount MountFunc) ServerOption {
	return func(o *Server) {
		o.mount = mount
	}
}

// WithAuthMiddleware configures the server with an authentication middleware.
func WithAuthMiddleware(m *authn.Middleware) ServerOption {
	return func(o *Server) {
		o.authMiddleware = m
	}
}

// WithPublicPaths configures unauthenticated paths.
func WithPublicPaths(paths []string) ServerOption {
	return func(o *Server) {
		if len(paths) == 0 {
			return
		}
		if o.publicPaths == nil {
			o.publicPaths = make(map[string]struct{}, len(paths))
		}
		for _, path := range paths {
			if path == "" {
				continue
			}
			o.publicPaths[path] = struct{}{}
		}
	}
}

// WithAllowedOrigins configures the allowed origins for CORS.
func WithAllowedOrigins(origins []string) ServerOption {
	return func(o *Server) {
		o.allowedOrigins = origins
	}
}

// NewServer creates a new HTTP server with the given options.
func NewServer(opts ...ServerOption) (*Server, error) {
	srv := &Server{
		address: ":8299",
	}
	for _, opt := range opts {
		opt(srv)
	}

	mux := http.NewServeMux()
	if srv.mount != nil {
		if err := srv.mount(mux); err != nil {
			return nil, err
		}
	}

	// Build Middleware Chain
	// The order is critical: H2C -> CORS -> Auth -> Mux
	var handler http.Handler = mux

	// Apply Authentication Middleware
	if srv.authMiddleware != nil {
		protected := srv.authMiddleware.Wrap(handler)
		if len(srv.publicPaths) == 0 {
			handler = protected
		} else {
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := srv.publicPaths[r.URL.Path]; ok {
					mux.ServeHTTP(w, r)
					return
				}
				protected.ServeHTTP(w, r)
			})
		}
	}

	// Apply CORS Middleware
	if len(srv.allowedOrigins) == 0 {
		// If no allowed origins are specified, allow all origins.
		handler = cors.AllowAll().Handler(handler)
	} else {
		// Strict CORS configuration
		c := cors.New(cors.Options{
			AllowedOrigins:   srv.allowedOrigins,
			AllowedMethods:   connectcors.AllowedMethods(),
			AllowedHeaders:   connectcors.AllowedHeaders(),
			ExposedHeaders:   connectcors.ExposedHeaders(),
			AllowCredentials: true,
			MaxAge:           7200,
		})
		handler = c.Handler(handler)
	}

	// HTTP/2 Support
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	// Configure HTTP Server
	srv.Server = &http.Server{
		Addr:              srv.address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		MaxHeaderBytes:    8 * 1024, // 8KiB
		Protocols:         protocols,
	}

	return srv, nil
}

// Start starts the HTTP server and blocks until the context is canceled.
func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return err
	}

	s.BaseContext = func(net.Listener) context.Context {
		return ctx
	}

	slog.Info("Server starting on",
		"address", listener.Addr().String(),
		"authMiddleware", s.authMiddleware != nil,
		"publicPaths", len(s.publicPaths),
		"allowedOrigins", s.allowedOrigins,
	)

	if err := s.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// Stop stops the HTTP server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("Gracefully shutting down HTTP server...")
	if err := s.Shutdown(ctx); err != nil {
		slog.Error("Graceful shutdown failed, forcing close", "error", err)
		return s.Close()
	}
	return nil
}
