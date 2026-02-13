package chisel

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"sync"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/core"
)

const port = 16598

type service struct {
	server *chserver.Server

	mu           sync.Mutex
	clusterHosts map[string]string
	usedHosts    map[string]struct{}
}

func NewService() core.TunnelProvider {
	return &service{
		server:       &chserver.Server{}, // Lazy initialization
		clusterHosts: map[string]string{},
		usedHosts:    map[string]struct{}{},
	}
}

var _ core.TunnelProvider = (*service)(nil)

func (c *service) Server() *chserver.Server {
	return c.server
}

func (c *service) RegisterCluster(cluster, user, pass string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	host, err := c.allocateHost(cluster)
	if err != nil {
		return "", err
	}

	allowed := fmt.Sprintf("^R:%s:%d(:.*)?$", regexp.QuoteMeta(host), port)
	if err := c.server.AddUser(user, pass, allowed); err != nil {
		c.releaseHost(host)
		return "", err
	}

	prevHost, hasPrevHost := c.clusterHosts[cluster]
	if hasPrevHost {
		c.releaseHost(prevHost)
	}
	c.clusterHosts[cluster] = host

	return fmt.Sprintf("%s:%d", host, port), nil
}

func (c *service) ResolveAddress(cluster string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	host, ok := c.clusterHosts[cluster]
	if !ok {
		return "", fmt.Errorf("cluster %s not registered", cluster)
	}

	return fmt.Sprintf("http://%s:%d", host, port), nil
}

func (c *service) allocateHost(cluster string) (string, error) {
	base := hashKey(cluster)
	for i := range uint32(1 << 24) {
		candidate := hostFromIndex(base + i)
		if _, exists := c.usedHosts[candidate]; exists {
			continue
		}
		c.usedHosts[candidate] = struct{}{}
		return candidate, nil
	}
	return "", fmt.Errorf("failed to allocate loopback host for cluster %s", cluster)
}

func (c *service) releaseHost(host string) {
	delete(c.usedHosts, host)
}

func hashKey(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

func hostFromIndex(v uint32) string {
	// 127.1.1.1 - 127.254.254.254, keep values away from 0/255.
	a := byte((v>>16)%254 + 1)
	b := byte((v>>8)%254 + 1)
	c := byte(v%254 + 1)
	return fmt.Sprintf("127.%d.%d.%d", a, b, c)
}
