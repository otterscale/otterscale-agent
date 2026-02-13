package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	chserver "github.com/jpillora/chisel/server"
)

type TunnelProvider interface {
	Server() *chserver.Server
	ListClusters() []string
	RegisterCluster(cluster, user, pass string) (string, error)
	ResolveAddress(cluster string) (string, error)
}

type TunnelConsumer interface {
	Register(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error)
}

type FleetUseCase struct {
	tunnel TunnelProvider
}

func NewFleetUseCase(tunnel TunnelProvider) *FleetUseCase {
	return &FleetUseCase{
		tunnel: tunnel,
	}
}

func (uc *FleetUseCase) ListClusters() []string {
	return uc.tunnel.ListClusters()
}

func (uc *FleetUseCase) RegisterCluster(cluster, agentID string) (endpoint, token string, err error) {
	token, err = uc.generateToken()
	if err != nil {
		return
	}
	endpoint, err = uc.tunnel.RegisterCluster(cluster, agentID, token)
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
