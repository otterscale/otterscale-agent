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

type Config struct {
	Cluster         string
	ServerURL       string
	TunnelServerURL string
	TunnelTimeout   time.Duration
}

type Agent struct {
	handler *Handler
	tunnel  core.TunnelConsumer
}

func NewAgent(handler *Handler, tunnel core.TunnelConsumer) *Agent {
	return &Agent{handler: handler, tunnel: tunnel}
}

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

	tunnelClt, err := tunnel.NewClient(ctx,
		tunnel.WithServerURL(cfg.ServerURL),
		tunnel.WithCluster(cfg.Cluster),
		tunnel.WithLocalPort(port),
		tunnel.WithKeepAlive(cfg.TunnelTimeout),
		tunnel.WithMaxRetryCount(6),
		tunnel.WithMaxRetryInterval(10*time.Second),
		tunnel.WithRegister(a.tunnel.Register),
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel client: %w", err)
	}
	return transport.Serve(ctx, httpSrv, tunnelClt)
}

func (a *Agent) findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port, nil
}
