// Package agent implements the agent-side runtime that reverse-proxies
// Kubernetes API requests received through a chisel tunnel.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
	"github.com/otterscale/otterscale-agent/internal/transport/pipe"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

// Config holds the runtime parameters for an Agent.
type Config struct {
	Cluster         string
	ServerURL       string
	TunnelServerURL string
	TunnelTimeout   time.Duration
}

// Agent binds a local HTTP reverse-proxy to a dynamically allocated
// port and exposes it to the control-plane via a chisel tunnel.
type Agent struct {
	handler *Handler
	tunnel  core.TunnelConsumer
}

// NewAgent returns an Agent wired to the given handler and tunnel
// consumer.
func NewAgent(handler *Handler, tunnel core.TunnelConsumer) *Agent {
	return &Agent{handler: handler, tunnel: tunnel}
}

// Run starts the agent. It creates an in-memory pipe listener for the
// HTTP server, a TCP bridge for chisel to forward to, and a tunnel
// client, then blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context, cfg Config) error {
	pl := pipe.NewListener()

	bridge, err := tunnel.NewBridge(pl)
	if err != nil {
		return fmt.Errorf("failed to create tunnel bridge: %w", err)
	}

	httpSrv, err := http.NewServer(
		http.WithListener(pl),
		http.WithMount(a.handler.Mount),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP server: %w", err)
	}

	tunnelClt, err := tunnel.NewClient(
		tunnel.WithServerURL(cfg.ServerURL),
		tunnel.WithTunnelServerURL(cfg.TunnelServerURL),
		tunnel.WithCluster(cfg.Cluster),
		tunnel.WithLocalPort(bridge.Port()),
		tunnel.WithKeepAlive(cfg.TunnelTimeout),
		tunnel.WithMaxRetryCount(6),
		tunnel.WithMaxRetryInterval(10*time.Second),
		tunnel.WithRegister(a.register()),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel client: %w", err)
	}
	return transport.Serve(ctx, httpSrv, bridge, tunnelClt)
}

// register wraps the register callback so that it returns the
// endpoint, fingerprint, and auth needed by the tunnel client.
func (a *Agent) register() tunnel.RegisterFunc {
	return func(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error) {
		reg, err := a.tunnel.Register(ctx, serverURL, cluster)
		if err != nil {
			return "", "", "", err
		}
		return reg.Endpoint, reg.Fingerprint, reg.Auth, nil
	}
}
