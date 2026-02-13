package core

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	chserver "github.com/jpillora/chisel/server"
)

type TunnelProvider interface {
	Server() *chserver.Server
	RegisterCluster(cluster, user, pass string) (string, error)
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

func (uc *FleetUseCase) RegisterCluster(cluster, agentID string) (host, token string, err error) {
	token, err = uc.generateToken()
	if err != nil {
		return
	}
	host, err = uc.tunnel.RegisterCluster(cluster, agentID, token)
	if err != nil {
		return
	}
	return
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
