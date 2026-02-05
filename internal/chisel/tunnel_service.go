package chisel

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"time"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/config"
)

// TunnelService manages a chisel reverse tunnel server and per-cluster users.
//
// Model (similar to Portainer):
//   - server runs chisel with Reverse=true
//   - each agent connects with basic auth and requests a reverse remote:
//     R:127.0.0.1:<cluster_tunnel_port>:127.0.0.1:<agent_api_port>
//   - server dials the local forwarded port (127.0.0.1:<cluster_tunnel_port>) to reach agent API.
type TunnelService struct {
	conf *config.Config

	mu          sync.Mutex
	server      *chserver.Server
	fingerprint string

	registered map[string]struct{} // clusterName -> registered user
}

func NewTunnelService(conf *config.Config) *TunnelService {
	return &TunnelService{
		conf:       conf,
		registered: map[string]struct{}{},
	}
}

func (s *TunnelService) Fingerprint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fingerprint
}

// Start starts the embedded chisel server (idempotent).
// It also pre-registers all configured cluster agent users.
func (s *TunnelService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		return nil
	}

	cfg := &chserver.Config{
		Reverse: true,
	}

	if keyFile := s.conf.TunnelServerKeyFile(); keyFile != "" {
		cfg.KeyFile = keyFile
	} else if keySeed := s.conf.TunnelServerKeySeed(); keySeed != "" {
		cfg.KeySeed = keySeed
	} else {
		return fmt.Errorf("tunnel.server.key_file or tunnel.server.key_seed must be set")
	}

	chSrv, err := chserver.NewServer(cfg)
	if err != nil {
		return err
	}

	host := s.conf.TunnelServerHost()
	port := s.conf.TunnelServerPort()
	if err := chSrv.Start(host, port); err != nil {
		return err
	}

	// Work-around Chisel default behavior:
	// if no users exist, Chisel will allow anyone to connect.
	// See Portainer: api/chisel/service.go
	disabledUser, disabledPass := randomCredentials()
	if err := chSrv.AddUser(disabledUser, disabledPass, "127.0.0.1"); err != nil {
		_ = chSrv.Close()
		return err
	}

	s.server = chSrv
	s.fingerprint = chSrv.GetFingerprint()

	// Pre-register users for all configured clusters so agents can connect immediately.
	for _, cluster := range s.conf.ClusterNames() {
		if err := s.registerClusterLocked(cluster); err != nil {
			_ = chSrv.Close()
			s.server = nil
			s.fingerprint = ""
			s.registered = map[string]struct{}{}
			return err
		}
	}

	return nil
}

// Stop closes the embedded chisel server and clears state.
// It is safe to call multiple times.
func (s *TunnelService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server == nil {
		return nil
	}

	err := s.server.Close()
	s.server = nil
	s.fingerprint = ""
	s.registered = map[string]struct{}{}
	return err
}

func (s *TunnelService) registerClusterLocked(cluster string) error {
	if _, ok := s.registered[cluster]; ok {
		return nil
	}

	user := s.conf.ClusterAgentAuthUser(cluster)
	pass := s.conf.ClusterAgentAuthPass(cluster)
	if user == "" || pass == "" {
		return fmt.Errorf("missing clusters.%s.agent.auth.user/pass", cluster)
	}

	tunnelPort := s.conf.ClusterAgentTunnelPort(cluster)
	if tunnelPort == 0 {
		return fmt.Errorf("missing clusters.%s.agent.tunnel_port", cluster)
	}

	// Only allow the agent to open the specific reverse port on loopback.
	// Chisel expects the authorized remote to match "R:host:port" (no local target part).
	allowed := fmt.Sprintf("^R:127.0.0.1:%d$", tunnelPort)

	if err := s.server.AddUser(user, pass, allowed); err != nil {
		return err
	}

	s.registered[cluster] = struct{}{}
	return nil
}

// AgentBaseURL returns the local base URL for reaching the agent API for a cluster.
// It waits briefly for the tunnel port to become reachable.
func (s *TunnelService) AgentBaseURL(cluster string, waitTimeout time.Duration) (string, error) {
	if err := s.Start(); err != nil {
		return "", err
	}

	s.mu.Lock()
	if err := s.registerClusterLocked(cluster); err != nil {
		s.mu.Unlock()
		return "", err
	}
	tunnelPort := s.conf.ClusterAgentTunnelPort(cluster)
	s.mu.Unlock()

	addr := fmt.Sprintf("127.0.0.1:%d", tunnelPort)
	if err := waitTCP(addr, waitTimeout); err != nil {
		return "", fmt.Errorf("tunnel not ready for cluster %q at %s: %w", cluster, addr, err)
	}

	return "http://" + addr, nil
}

func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 250 * time.Millisecond}
		conn, err := d.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func randomCredentials() (string, string) {
	buf := make([]byte, 18)
	_, _ = rand.Read(buf) // best-effort; if it fails, base64 will still produce something
	s := base64.RawStdEncoding.EncodeToString(buf)
	// split into two short-ish strings
	if len(s) < 16 {
		return s, s
	}
	return s[:8], s[8:16]
}
