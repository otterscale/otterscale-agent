package chisel

import (
	"fmt"
	"sync"

	chserver "github.com/jpillora/chisel/server"

	"github.com/otterscale/otterscale-agent/internal/core"
)

type service struct {
	server  *chserver.Server
	portMap sync.Map
}

func NewService() core.TunnelProvider {
	return &service{
		server: &chserver.Server{}, // Lazy initialization
	}
}

var _ core.TunnelProvider = (*service)(nil)

func (c *service) Server() *chserver.Server {
	return c.server
}

func (c *service) RegisterCluster(cluster, user, pass string, tunnelPort int) error {
	allowed := fmt.Sprintf("^R:127.0.0.1:%d(:.*)?$", tunnelPort)
	if err := c.server.AddUser(user, pass, allowed); err != nil {
		return err
	}
	c.portMap.Store(cluster, tunnelPort)
	return nil
}

func (c *service) ResolveAddress(cluster string) (string, error) {
	port, ok := c.portMap.Load(cluster)
	if !ok {
		return "", fmt.Errorf("cluster %s not registered", cluster)
	}

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}
