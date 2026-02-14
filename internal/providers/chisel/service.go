// Package chisel implements core.TunnelProvider using jpillora/chisel.
//
// Each registered cluster is assigned a unique loopback address in
// the 127.x.x.x range so that chisel can route reverse-tunnel traffic
// to the correct agent without port conflicts.
package chisel

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/pki"
	"github.com/otterscale/otterscale-agent/internal/transport"
	"github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

// tunnelPort is the fixed port shared by all cluster tunnels.
// Each cluster is differentiated by its loopback host, not its port.
const tunnelPort = 16598

// maxHosts is the total number of unique loopback addresses available
// in the range 127.1.1.1 – 127.254.254.254 (octets 0 and 255 are
// avoided).
const maxHosts = 254 * 254 * 254

// Service manages the mapping between cluster names and unique
// loopback addresses, and provisions chisel users for each agent.
// It implements core.TunnelProvider and additionally exposes the
// underlying chisel server via ServerRef() for transport-layer init.
type Service struct {
	server atomic.Pointer[chserver.Server]
	ca     *pki.CA
	log    *slog.Logger

	mu        sync.RWMutex
	clusters  map[string]core.Cluster // cluster name -> tunnel state
	usedHosts map[string]struct{}     // set of allocated hosts
}

// NewService returns a new Service backed by chisel. The CA is
// required for signing agent CSRs and must be provided at
// construction time (dependency injection).
// The underlying chisel server is lazily initialized by the tunnel
// transport layer; see tunnel.NewServer.
func NewService(ca *pki.CA) *Service {
	return &Service{
		ca:        ca,
		log:       slog.Default().With("component", "tunnel-provider"),
		clusters:  make(map[string]core.Cluster),
		usedHosts: make(map[string]struct{}),
	}
}

var _ core.TunnelProvider = (*Service)(nil)

// ServerRef returns a pointer to the atomic chisel server reference.
// The tunnel transport stores the fully initialized server into this
// reference at startup so that both sides share the same instance.
// This method is intentionally NOT part of core.TunnelProvider to keep
// the domain layer free of chisel dependencies.
func (s *Service) ServerRef() *atomic.Pointer[chserver.Server] {
	return &s.server
}

// CA returns the CA used to sign agent CSRs and generate server
// certificates. This is provided at construction time via DI.
func (s *Service) CA() *pki.CA {
	return s.ca
}

// CACertPEM returns the PEM-encoded CA certificate so that agents
// can verify the tunnel server's identity via mTLS.
func (s *Service) CACertPEM() []byte {
	return s.ca.CertPEM()
}

// ListClusters returns the names of all currently registered clusters.
func (s *Service) ListClusters() map[string]core.Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return maps.Clone(s.clusters)
}

// RegisterCluster validates and signs the agent's CSR, associates a
// cluster with a unique loopback host, creates a chisel user with a
// password derived from the signed certificate, and returns the
// tunnel endpoint and the PEM-encoded signed certificate.
//
// If the cluster was previously registered, the old host allocation
// is released first so that re-registration always moves the cluster
// to a fresh address.
func (s *Service) RegisterCluster(_ context.Context, cluster, agentID, agentVersion string, csrPEM []byte) (string, []byte, error) {
	// Sign the agent's CSR with the internal CA.
	certPEM, err := s.ca.SignCSR(csrPEM)
	if err != nil {
		return "", nil, fmt.Errorf("sign CSR: %w", err)
	}

	// Derive the chisel password from the signed certificate so
	// that both server and agent can compute it independently.
	auth, err := pki.DeriveAuth(agentID, certPEM)
	if err != nil {
		return "", nil, fmt.Errorf("derive auth: %w", err)
	}
	_, pass, ok := parseAuth(auth)
	if !ok {
		return "", nil, fmt.Errorf("invalid auth format: expected user:pass, got %q", auth)
	}

	srv := s.server.Load()
	if srv == nil {
		return "", nil, &core.ErrNotReady{Subsystem: "chisel server"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Release the previous host and user for this cluster, if any,
	// so that stale credentials do not accumulate in chisel.
	if prev, ok := s.clusters[cluster]; ok {
		srv.DeleteUser(prev.User)
		s.releaseHost(prev.Host)
		delete(s.clusters, cluster)
	}

	host, err := s.allocateHost(cluster)
	if err != nil {
		return "", nil, err
	}

	// Restrict the user to reverse-tunnelling only the allocated
	// host:port combination. The regex anchors prevent the agent
	// from binding arbitrary endpoints.
	allowed := fmt.Sprintf("^R:%s:%d(:.*)?$", regexp.QuoteMeta(host), tunnelPort)
	if err := srv.AddUser(agentID, pass, allowed); err != nil {
		s.releaseHost(host)
		return "", nil, err
	}

	s.clusters[cluster] = core.Cluster{
		Host:         host,
		User:         agentID,
		AgentVersion: agentVersion,
	}

	return fmt.Sprintf("%s:%d", host, tunnelPort), certPEM, nil
}

// DeregisterCluster removes a cluster's tunnel allocation, deleting
// the chisel user and releasing the loopback host. It is a no-op if
// the cluster is not currently registered.
func (s *Service) DeregisterCluster(cluster string) {
	srv := s.server.Load()
	if srv == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.clusters[cluster]
	if !ok {
		return
	}
	srv.DeleteUser(entry.User)
	s.releaseHost(entry.Host)
	delete(s.clusters, cluster)
}

// ResolveAddress returns the HTTP base URL for the given cluster's
// tunnel endpoint. Returns an error if the cluster is not registered.
func (s *Service) ResolveAddress(_ context.Context, cluster string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.clusters[cluster]
	if !ok {
		return "", &core.ErrClusterNotFound{Cluster: cluster}
	}

	return fmt.Sprintf("http://%s:%d", entry.Host, tunnelPort), nil
}

// allocateHost picks a unique loopback address for the given cluster
// by hashing the name and probing linearly until an unused address is
// found. Must be called with mu held.
func (s *Service) allocateHost(cluster string) (string, error) {
	base := hashKey(cluster)
	for i := range uint32(maxHosts) {
		candidate := hostFromIndex((base + i) % uint32(maxHosts))
		if _, exists := s.usedHosts[candidate]; exists {
			continue
		}
		s.usedHosts[candidate] = struct{}{}
		return candidate, nil
	}
	return "", fmt.Errorf("exhausted loopback address space (%d hosts)", maxHosts)
}

// releaseHost returns a previously allocated host to the pool.
// Must be called with mu held.
func (s *Service) releaseHost(host string) {
	delete(s.usedHosts, host)
}

// hashKey returns a deterministic 32-bit hash of the given key using
// FNV-1a so that the same cluster name tends to land on the same
// starting index.
func hashKey(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

// hostFromIndex maps a linear index (0 – maxHosts-1) to a unique
// loopback address in the range 127.1.1.1 – 127.254.254.254.
// Octets 0 and 255 are avoided to stay clear of network/broadcast
// conventions.
func hostFromIndex(idx uint32) string {
	a := idx / (254 * 254)
	b := (idx / 254) % 254
	c := idx % 254
	return fmt.Sprintf("127.%d.%d.%d", a+1, b+1, c+1)
}

// BuildTunnelListener generates a server TLS certificate for the
// given host, writes the mTLS materials to a temporary directory,
// and returns a fully configured tunnel transport.Listener. The
// caller is responsible for starting the listener via transport.Serve.
// The temporary certificate files are cleaned up when the listener
// stops.
func (s *Service) BuildTunnelListener(address, host string) (transport.Listener, error) {
	serverCert, serverKey, err := s.ca.GenerateServerCert(host)
	if err != nil {
		return nil, fmt.Errorf("generate server cert: %w", err)
	}

	certDir, err := os.MkdirTemp("", "otterscale-tls-server-*")
	if err != nil {
		return nil, fmt.Errorf("create cert dir: %w", err)
	}

	caFile := filepath.Join(certDir, "ca.pem")
	certFile := filepath.Join(certDir, "cert.pem")
	keyFile := filepath.Join(certDir, "key.pem")

	if err := os.WriteFile(caFile, s.ca.CertPEM(), 0600); err != nil {
		os.RemoveAll(certDir)
		return nil, fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(certFile, serverCert, 0600); err != nil {
		os.RemoveAll(certDir)
		return nil, fmt.Errorf("write server cert: %w", err)
	}
	if err := os.WriteFile(keyFile, serverKey, 0600); err != nil {
		os.RemoveAll(certDir)
		return nil, fmt.Errorf("write server key: %w", err)
	}

	slog.Info("tunnel CA initialized", "subject", "otterscale-ca")

	tunnelSrv, err := tunnel.NewServer(
		tunnel.WithAddress(address),
		tunnel.WithTLSCert(certFile),
		tunnel.WithTLSKey(keyFile),
		tunnel.WithTLSCA(caFile),
		tunnel.WithServer(s.ServerRef()),
	)
	if err != nil {
		os.RemoveAll(certDir)
		return nil, fmt.Errorf("create tunnel server: %w", err)
	}

	return &tunnelListenerWithCleanup{
		Listener: tunnelSrv,
		certDir:  certDir,
	}, nil
}

// tunnelListenerWithCleanup wraps a transport.Listener and removes
// the temporary TLS certificate directory when stopped.
type tunnelListenerWithCleanup struct {
	transport.Listener
	certDir string
}

func (l *tunnelListenerWithCleanup) Stop(ctx context.Context) error {
	err := l.Listener.Stop(ctx)
	os.RemoveAll(l.certDir)
	return err
}

// BuildHealthListener returns a transport.Listener that periodically
// health-checks registered tunnel endpoints and deregisters
// disconnected clusters.
func (s *Service) BuildHealthListener() transport.Listener {
	return NewHealthCheckListener(s)
}

// parseAuth splits a "user:pass" string into its components.
func parseAuth(auth string) (user, pass string, ok bool) {
	return strings.Cut(auth, ":")
}
