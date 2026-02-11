package chisel

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
)

type chiselService struct {
	server  *chserver.Server
	portMap sync.Map
}

func NewChiselService(conf *config.Config) (core.TunnelProvider, error) {
	keySeed := conf.ServerTunnelKeySeed()

	svc := &chiselService{}

	// Start an internal registration HTTP handler on a random local port.
	// Chisel's Proxy feature will forward non-WebSocket requests to it.
	regListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen for registration handler: %w", err)
	}
	regAddr := regListener.Addr().String()
	regURL := fmt.Sprintf("http://%s", regAddr)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", svc.handleRegister)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("OK\n"))
	})

	go func() {
		slog.Info("Registration handler listening", "address", regAddr)
		if err := http.Serve(regListener, mux); err != nil && err != http.ErrServerClosed {
			slog.Error("Registration handler failed", "error", err)
		}
	}()

	cfg := &chserver.Config{
		Reverse: true,
		KeySeed: keySeed,
		Proxy:   regURL,
	}

	chServer, err := chserver.NewServer(cfg)
	if err != nil {
		return nil, err
	}

	svc.server = chServer
	return svc, nil
}

var _ core.TunnelProvider = (*chiselService)(nil)

func (c *chiselService) Start(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	return c.server.Start(host, port)
}

func (c *chiselService) RegisterCluster(cluster, user, pass string, tunnelPort int) error {
	allowed := fmt.Sprintf("^R:127.0.0.1:%d(:.*)?$", tunnelPort)
	if err := c.server.AddUser(user, pass, allowed); err != nil {
		return err
	}
	c.portMap.Store(cluster, tunnelPort)
	return nil
}

func (c *chiselService) GetTunnelAddress(cluster string) (string, error) {
	port, ok := c.portMap.Load(cluster)
	if !ok {
		return "", fmt.Errorf("cluster %s not registered", cluster)
	}

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

func (c *chiselService) GetFingerprint() string {
	return c.server.GetFingerprint()
}

// handleRegister handles POST /v1/register requests from agents.
// It extracts Basic Auth credentials and JSON body to register the cluster,
// then returns the server fingerprint so the agent can verify the tunnel.
func (c *chiselService) handleRegister(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok {
		http.Error(w, "unauthorized: missing basic auth", http.StatusUnauthorized)
		return
	}

	var req struct {
		Cluster    string `json:"cluster"`
		TunnelPort int    `json:"tunnel_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Cluster == "" || req.TunnelPort == 0 {
		http.Error(w, "bad request: cluster and tunnel_port are required", http.StatusBadRequest)
		return
	}

	if err := c.RegisterCluster(req.Cluster, user, pass, req.TunnelPort); err != nil {
		slog.Error("Failed to register cluster", "cluster", req.Cluster, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	slog.Info("Cluster registered", "cluster", req.Cluster, "tunnelPort", req.TunnelPort, "user", user)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"fingerprint": c.GetFingerprint(),
	})
}
