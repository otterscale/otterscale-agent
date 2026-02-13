// Package chisel implements core.TunnelProvider using jpillora/chisel.
//
// Each registered cluster is assigned a unique loopback address in
// the 127.x.x.x range so that chisel can route reverse-tunnel traffic
// to the correct agent without port conflicts.
package chisel

import (
	"fmt"
	"hash/fnv"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"sync"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// tunnelPort is the fixed port shared by all cluster tunnels.
// Each cluster is differentiated by its loopback host, not its port.
const tunnelPort = 16598

// clusterEntry holds the per-cluster tunnel state: the allocated
// loopback host and the chisel user name.
type clusterEntry struct {
	host string // unique 127.x.x.x loopback address
	user string // chisel user name
}

// Service manages the mapping between cluster names and unique
// loopback addresses, and provisions chisel users for each agent.
// It implements core.TunnelProvider and additionally exposes the
// underlying chisel server via Server() for transport-layer init.
type Service struct {
	server *chserver.Server
	log    *slog.Logger

	mu        sync.RWMutex
	clusters  map[string]*clusterEntry // cluster name -> tunnel state
	usedHosts map[string]struct{}      // set of allocated hosts
}

// NewService returns a new Service backed by chisel.
// The underlying chisel server is lazily initialized by the tunnel
// transport layer; see tunnel.NewServer.
func NewService() *Service {
	return &Service{
		server:    &chserver.Server{},
		log:       slog.Default().With("component", "tunnel-provider"),
		clusters:  make(map[string]*clusterEntry),
		usedHosts: make(map[string]struct{}),
	}
}

var _ core.TunnelProvider = (*Service)(nil)

// Server returns the shared chisel server pointer. The tunnel
// transport writes the fully initialized server into this pointer
// at startup so both sides share the same instance. This method is
// intentionally NOT part of core.TunnelProvider to keep the domain
// layer free of chisel dependencies.
func (s *Service) Server() *chserver.Server {
	return s.server
}

// Fingerprint returns the TLS fingerprint of the tunnel server so
// that agents can verify server identity without a CA.
func (s *Service) Fingerprint() string {
	return s.server.GetFingerprint()
}

// ListClusters returns the names of all currently registered clusters.
func (s *Service) ListClusters() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return slices.Collect(maps.Keys(s.clusters))
}

// RegisterCluster associates a cluster with a unique loopback host,
// creates a chisel user with credentials (user/pass), and returns the
// tunnel endpoint (host:port).
//
// If the cluster was previously registered, the old host allocation is
// released so that re-registration always moves the cluster to a fresh
// address.
func (s *Service) RegisterCluster(cluster, user, pass string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	host, err := s.allocateHost(cluster)
	if err != nil {
		return "", err
	}

	// Remove the previous user and host for this cluster, if any,
	// so that stale credentials do not accumulate in chisel.
	if prev, ok := s.clusters[cluster]; ok {
		s.server.DeleteUser(prev.user)
		s.releaseHost(prev.host)
	}

	// Restrict the user to reverse-tunnelling only the allocated
	// host:port combination. The regex anchors prevent the agent
	// from binding arbitrary endpoints.
	allowed := fmt.Sprintf("^R:%s:%d(:.*)?$", regexp.QuoteMeta(host), tunnelPort)
	if err := s.server.AddUser(user, pass, allowed); err != nil {
		s.releaseHost(host)
		return "", err
	}

	s.clusters[cluster] = &clusterEntry{
		host: host,
		user: user,
	}

	return fmt.Sprintf("%s:%d", host, tunnelPort), nil
}

// DeregisterCluster removes a cluster's tunnel allocation, deleting
// the chisel user and releasing the loopback host. It is a no-op if
// the cluster is not currently registered.
func (s *Service) DeregisterCluster(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.clusters[cluster]
	if !ok {
		return
	}
	s.server.DeleteUser(entry.user)
	s.releaseHost(entry.host)
	delete(s.clusters, cluster)
}

// ResolveAddress returns the HTTP base URL for the given cluster's
// tunnel endpoint. Returns an error if the cluster is not registered.
func (s *Service) ResolveAddress(cluster string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.clusters[cluster]
	if !ok {
		return "", fmt.Errorf("cluster %s not registered", cluster)
	}

	return fmt.Sprintf("http://%s:%d", entry.host, tunnelPort), nil
}

// allocateHost picks a unique loopback address for the given cluster
// by hashing the name and probing linearly until an unused address is
// found. Must be called with mu held.
func (s *Service) allocateHost(cluster string) (string, error) {
	base := hashKey(cluster)
	for i := range uint32(1 << 24) {
		candidate := hostFromIndex(base + i)
		if _, exists := s.usedHosts[candidate]; exists {
			continue
		}
		s.usedHosts[candidate] = struct{}{}
		return candidate, nil
	}
	return "", fmt.Errorf("failed to allocate loopback host for cluster %s", cluster)
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

// hostFromIndex maps a 32-bit index to a unique loopback address in
// the range 127.1.1.1 – 127.254.254.254. Octets 0 and 255 are
// avoided to stay clear of network/broadcast conventions.
func hostFromIndex(v uint32) string {
	a := byte((v>>16)%254 + 1)
	b := byte((v>>8)%254 + 1)
	c := byte(v%254 + 1)
	return fmt.Sprintf("127.%d.%d.%d", a, b, c)
}
