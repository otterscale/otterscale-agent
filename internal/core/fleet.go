package core

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"

	chserver "github.com/jpillora/chisel/server"
)

type TunnelProvider interface {
	Server() *chserver.Server
	RegisterCluster(cluster, user, pass string, tunnelPort int) error
	ResolveAddress(cluster string) (string, error)
}

type FleetUseCase struct {
	tunnel TunnelProvider
}

func NewFleetUseCase(tunnel TunnelProvider) *FleetUseCase {
	return &FleetUseCase{
		tunnel: tunnel,
	}
}

func (uc *FleetUseCase) RegisterCluster(cluster, agentID string) (string, int, error) {
	token, err := uc.generateToken()
	if err != nil {
		return "", 0, err
	}

	freePort, err := uc.findFreePort()
	if err != nil {
		return "", 0, err
	}

	if err := uc.tunnel.RegisterCluster(cluster, agentID, token, freePort); err != nil {
		return "", 0, err
	}

	return token, freePort, nil
}

func (uc *FleetUseCase) Fingerprint() string {
	return uc.tunnel.Server().GetFingerprint()
}

func (uc *FleetUseCase) generateToken() (string, error) {
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (uc *FleetUseCase) findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port, nil
}
