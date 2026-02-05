package cmd

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/spf13/cobra"

	"github.com/otterscale/otterscale-agent/internal/chisel"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
	"github.com/otterscale/otterscale-agent/internal/leader"
	"github.com/otterscale/otterscale-agent/internal/mux"
)

func NewServer(conf *config.Config, hub *mux.Hub, tunnels *chisel.TunnelService, elector *leader.Elector) *cobra.Command {
	var address, configPath string

	cmd := &cobra.Command{
		Use:     "server",
		Short:   "Start server that provides gRPC and HTTP endpoints for the core services",
		Example: "otterscale server --address=:8299 --config=otterscale.yaml",
		RunE: func(_ *cobra.Command, _ []string) error {
			if os.Getenv(containerEnvVar) != "" {
				address = defaultContainerAddress
				configPath = defaultContainerConfigPath
				slog.Info("Container environment detected, using default configuration", "address", address, "config", configPath)
			}

			slog.Info("Loading configuration file", "path", configPath)
			if err := conf.Load(configPath); err != nil {
				return err
			}

			// Leader election gates tunnel server start/stop.
			if elector != nil {
				go func() {
					err := elector.Run(context.Background(),
						func(_ context.Context) {
							if err := tunnels.Start(); err != nil {
								slog.Error("Tunnel start failed", "err", err)
								return
							}
							slog.Info("Became leader; tunnel server started", "fingerprint", tunnels.Fingerprint(), "addr", conf.TunnelServerAddr())
						},
						func() {
							if err := tunnels.Stop(); err != nil {
								slog.Error("Tunnel stop failed", "err", err)
							}
							slog.Info("Lost leadership; tunnel server stopped")
						},
					)
					if err != nil {
						slog.Error("Leader election stopped with error", "err", err)
					}
				}()
			} else {
				// If no elector is wired, fallback to single-instance behavior.
				if err := tunnels.Start(); err != nil {
					return err
				}
				slog.Info("Tunnel server started", "fingerprint", tunnels.Fingerprint(), "addr", conf.TunnelServerAddr())
			}

			openTelemetryInterceptor, err := otelconnect.NewInterceptor()
			if err != nil {
				return err
			}

			impersonationInterceptor, err := impersonation.NewInterceptor(conf)
			if err != nil {
				return err
			}

			opts := []connect.HandlerOption{
				connect.WithInterceptors(openTelemetryInterceptor, impersonationInterceptor),
			}

			wrapped := withLeaderForwarding(hub, elector, address)
			return startHTTPServer(context.Background(), address, wrapped, opts...)
		},
	}

	cmd.Flags().StringVarP(
		&address,
		"address",
		"a",
		":0",
		"Address for server to listen on",
	)

	cmd.Flags().StringVarP(
		&configPath,
		"config",
		"c",
		"otterscale.yaml",
		"Config path for server to load",
	)

	return cmd
}

type leaderForwardingHandler struct {
	next    handler
	elector *leader.Elector
	port    int
}

func withLeaderForwarding(next handler, elector *leader.Elector, listenAddr string) handler {
	if elector == nil {
		return next
	}
	port := parsePort(listenAddr)
	// If port is 0 (ephemeral), we can't reliably forward to leader pod by port.
	// In k8s this should be a fixed container port.
	if port == 0 {
		slog.Warn("Leader forwarding disabled due to ephemeral port; set --address to a fixed port", "address", listenAddr)
		return next
	}
	return &leaderForwardingHandler{next: next, elector: elector, port: port}
}

func (h *leaderForwardingHandler) RegisterHandlers(opts []connect.HandlerOption) error {
	return h.next.RegisterHandlers(opts)
}

func (h *leaderForwardingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.elector == nil || h.elector.IsLeader() {
		h.next.ServeHTTP(w, r)
		return
	}

	// Resolve leader pod IP and proxy the request to leader directly.
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()

	leaderPodName, err := h.elector.LeaderPodName(ctx)
	if err != nil {
		http.Error(w, "not leader", http.StatusServiceUnavailable)
		return
	}
	if leaderPodName == h.elector.Identity() {
		// Avoid proxy loop during transitions.
		http.Error(w, "not leader", http.StatusServiceUnavailable)
		return
	}

	ip, err := h.elector.LeaderPodIP(ctx)
	if err != nil {
		http.Error(w, "not leader", http.StatusServiceUnavailable)
		return
	}

	u, err := url.Parse("http://" + net.JoinHostPort(ip, strconv.Itoa(h.port)))
	if err != nil {
		http.Error(w, "not leader", http.StatusServiceUnavailable)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		// Preserve original Host header for upstream auth/routing consistency.
		req.Host = u.Host
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, e error) {
		_ = e
		http.Error(rw, "not leader", http.StatusServiceUnavailable)
	}

	proxy.ServeHTTP(w, r)
}

func parsePort(addr string) int {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			host = ""
			portStr = strings.TrimPrefix(addr, ":")
		} else {
			return 0
		}
	}
	_ = host
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}
