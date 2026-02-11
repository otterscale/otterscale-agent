package chisel

import (
	"fmt"
	"net"
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

	cfg := &chserver.Config{
		Reverse: true,
		KeySeed: keySeed,
	}

	chServer, err := chserver.NewServer(cfg)
	if err != nil {
		return nil, err
	}

	return &chiselService{
		server: chServer,
	}, nil
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
