package core

type TunnelProvider interface {
	Start(address string) error
	RegisterCluster(cluster, user, pass string, tunnelPort int) error
	GetTunnelAddress(cluster string) (string, error)
	GetFingerprint() string
}
