// Package core defines the domain interfaces and use-case logic for
// the otterscale agent. Infrastructure adapters (chisel, kubernetes,
// otterscale) implement the interfaces declared here.
package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// TunnelProvider is the server-side abstraction for managing reverse
// tunnels. It allocates unique endpoints per cluster and provisions
// tunnel users for each connecting agent.
type TunnelProvider interface {
	// Fingerprint returns the TLS fingerprint of the tunnel server
	// so that agents can verify server identity without a CA.
	Fingerprint() string
	// ListClusters returns the names of all registered clusters.
	ListClusters() []string
	// RegisterCluster creates a tunnel user and returns the allocated endpoint.
	RegisterCluster(cluster, user, pass string) (string, error)
	// ResolveAddress returns the HTTP base URL for the given cluster.
	ResolveAddress(cluster string) (string, error)
}

// TunnelConsumer is the agent-side abstraction for registering with
// the fleet server and obtaining tunnel credentials.
type TunnelConsumer interface {
	// Register calls the fleet API and returns the endpoint, TLS
	// fingerprint, and authentication string for the tunnel.
	Register(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error)
}

// FleetUseCase orchestrates cluster registration on the server side.
// It generates one-time tokens for agents and delegates the actual
// tunnel setup to the TunnelProvider.
type FleetUseCase struct {
	tunnel TunnelProvider
}

// NewFleetUseCase returns a FleetUseCase backed by the given
// TunnelProvider.
func NewFleetUseCase(tunnel TunnelProvider) *FleetUseCase {
	return &FleetUseCase{
		tunnel: tunnel,
	}
}

// ListClusters returns the names of all currently registered clusters.
func (uc *FleetUseCase) ListClusters() []string {
	return uc.tunnel.ListClusters()
}

// RegisterCluster generates a fresh token, registers the agent with
// the tunnel provider, and returns the tunnel endpoint together with
// the token the agent should use for authentication.
func (uc *FleetUseCase) RegisterCluster(cluster, agentID string) (endpoint, token string, err error) {
	token, err = uc.generateToken()
	if err != nil {
		return
	}
	endpoint, err = uc.tunnel.RegisterCluster(cluster, agentID, token)
	return
}

// Fingerprint returns the TLS fingerprint of the tunnel server so
// that agents can verify server identity without a CA.
func (uc *FleetUseCase) Fingerprint() string {
	return uc.tunnel.Fingerprint()
}

// generateToken produces a 32-byte cryptographically random token
// encoded as URL-safe base64 (no padding).
func (uc *FleetUseCase) generateToken() (string, error) {
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
