package mux

import (
	"context"
	"fmt"
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
type Option func(*serverOptions)

type serverOptions struct {
	authMiddleware *authn.Middleware
	allowedOrigins []string
}

// WithAuthMiddleware configures the server with an authentication middleware.
func WithAuthMiddleware(m *authn.Middleware) Option {
	return func(o *serverOptions) {
		o.authMiddleware = m
	}
}

// WithAllowedOrigins configures the allowed origins for CORS.
func WithAllowedOrigins(origins []string) Option {
	return func(o *serverOptions) {
		o.allowedOrigins = origins
	}
}

// Run starts the HTTP server and blocks until the context is canceled.
func Run(
	ctx context.Context,
	address string,
	mount MountFunc,
	opts ...Option,
) error {
	// Initialize Default Options
	options := &serverOptions{
		authMiddleware: nil,
		allowedOrigins: []string{},
	}

	// Apply Functional Options
	for _, opt := range opts {
		opt(options)
	}

	// Create the Root Mux
	mux := http.NewServeMux()

	// Mount handlers
	// Execute the user-provided function to register routes onto the mux.
	if err := mount(mux); err != nil {
		return err
	}

	// Build Middleware Chain
	// The order is critical: H2C -> CORS -> Auth -> Mux
	var handler http.Handler = mux

	// Apply Authentication Middleware
	if options.authMiddleware != nil {
		handler = options.authMiddleware.Wrap(handler)
	}

	// Apply CORS Middleware
	if len(options.allowedOrigins) == 0 {
		// If no allowed origins are specified, allow all origins.
		handler = cors.AllowAll().Handler(handler)
	} else {
		// Strict CORS configuration
		c := cors.New(cors.Options{
			AllowedOrigins:   options.allowedOrigins,
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
	srv := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		MaxHeaderBytes:    8 * 1024, // 8KiB
		Protocols:         protocols,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	// Listen for incoming connections
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}

	// Use a buffered channel to prevent blocking if we return early
	serverErr := make(chan error, 1)

	slog.Info("Server starting on",
		"address", listener.Addr().String(),
		"authMiddleware", options.authMiddleware != nil,
		"allowedOrigins", options.allowedOrigins,
	)

	// Start Serving in Background
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Block and Wait for Shutdown or Error
	select {
	case err := <-serverErr:
		return fmt.Errorf("server runtime error: %w", err)

	case <-ctx.Done():
		slog.Info("Gracefully shutting down HTTP server...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("Graceful shutdown failed, forcing close", "error", err)
			return srv.Close()
		}

		slog.Info("HTTP server stopped gracefully")
		return nil
	}
}
