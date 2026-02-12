package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
	"github.com/rs/cors"
)

type handler interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
	RegisterHandlers(opts ...connect.HandlerOption) error
}

func startHTTPServer(ctx context.Context, handler handler, authMiddleware *authn.Middleware, address string, allowedOrigins []string, opts ...connect.HandlerOption) error {
	if err := handler.RegisterHandlers(opts...); err != nil {
		return err
	}

	var corsHandler *cors.Cors
	if len(allowedOrigins) == 0 {
		corsHandler = cors.AllowAll()
	} else {
		corsHandler = cors.New(cors.Options{
			AllowedOrigins:   allowedOrigins,
			AllowedMethods:   connectcors.AllowedMethods(),
			AllowedHeaders:   connectcors.AllowedHeaders(),
			ExposedHeaders:   connectcors.ExposedHeaders(),
			AllowCredentials: true,
		})
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Addr:              address,
		Handler:           corsHandler.Handler(authMiddleware.Wrap(handler)),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		MaxHeaderBytes:    8 * 1024, // 8KiB
		Protocols:         protocols,
	}

	listener, err := net.Listen("tcp", address) //nolint:noctx // context not needed for Listen
	if err != nil {
		return err
	}

	serverErr := make(chan error, 1)

	slog.Info("Server starting on", "address", listener.Addr().String())
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)

	case <-ctx.Done():
		slog.Info("Gracefully shutting down HTTP server...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("HTTP server forced to shutdown: %w", err)
		}

		slog.Info("HTTP server stopped gracefully")
		return nil
	}
}
