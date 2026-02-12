package core

import chserver "github.com/jpillora/chisel/server"

type TunnelProvider interface {
	Server() *chserver.Server
	RegisterCluster(cluster, user, pass string, tunnelPort int) error
	ResolveAddress(cluster string) (string, error)
}
