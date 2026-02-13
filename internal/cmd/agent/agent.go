// Package agent implements the agent-side runtime that reverse-proxies
// Kubernetes API requests received through a chisel tunnel.
package agent

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/http"
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

// Run starts the agent. It allocates a free loopback port, creates an
// HTTP server and a tunnel client, then blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context, cfg Config) error {
	port, err := a.findFreePort()
	if err != nil {
		return fmt.Errorf("failed to find free port: %w", err)
	}

	httpSrv, err := http.NewServer(
		http.WithAddress(fmt.Sprintf("127.0.0.1:%d", port)),
		http.WithMount(a.handler.Mount),
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP server: %w", err)
	}

	tunnelClt, err := tunnel.NewClient(
		tunnel.WithServerURL(cfg.ServerURL),
		tunnel.WithTunnelServerURL(cfg.TunnelServerURL),
		tunnel.WithCluster(cfg.Cluster),
		tunnel.WithLocalPort(port),
		tunnel.WithKeepAlive(cfg.TunnelTimeout),
		tunnel.WithMaxRetryCount(6),
		tunnel.WithMaxRetryInterval(10*time.Second),
		tunnel.WithRegister(a.register()),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel client: %w", err)
	}
	return transport.Serve(ctx, httpSrv, tunnelClt)
}

// findFreePort asks the OS for an available TCP port on localhost by
// binding to port 0 and immediately closing the listener.
func (a *Agent) findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port, nil
}

// register wraps the register callback so that the proxy token returned
// by the fleet server is forwarded to the handler's middleware
// after every successful registration or re-registration.
func (a *Agent) register() tunnel.RegisterFunc {
	return func(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error) {
		reg, err := a.tunnel.Register(ctx, serverURL, cluster)
		if err != nil {
			return "", "", "", err
		}
		a.handler.SetProxyToken(reg.ProxyToken)
		return reg.Endpoint, reg.Fingerprint, reg.Auth, nil
	}
}
