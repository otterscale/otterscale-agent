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

// ProxyTokenHeader is the HTTP header used to authenticate requests
// passing through the tunnel reverse proxy.
const ProxyTokenHeader = "X-Proxy-Token"

// TunnelProvider is the server-side abstraction for managing reverse
// tunnels. It allocates unique endpoints per cluster and provisions
// tunnel users for each connecting agent.
type TunnelProvider interface {
	// Fingerprint returns the TLS fingerprint of the tunnel server
	// so that agents can verify server identity without a CA.
	Fingerprint() string
	// ListClusters returns the names of all registered clusters.
	ListClusters() []string
	// RegisterCluster creates a tunnel user and returns the allocated
	// endpoint and a proxy token the server must present on every
	// proxied request.
	RegisterCluster(cluster, user, pass, proxyToken string) (string, error)
	// ResolveAddress returns the HTTP base URL for the given cluster.
	ResolveAddress(cluster string) (string, error)
	// ProxyToken returns the current proxy token for the given cluster.
	ProxyToken(cluster string) (string, error)
}

// TunnelConsumer is the agent-side abstraction for registering with
// the fleet server and obtaining tunnel credentials.
type TunnelConsumer interface {
	// Register calls the fleet API and returns the endpoint, TLS
	// fingerprint, authentication string, and proxy token for the
	// tunnel.
	Register(ctx context.Context, serverURL, cluster string) (Registration, error)
}

// Registration holds the credentials and connection details returned
// by the fleet server after a successful cluster registration.
type Registration struct {
	// Endpoint is the tunnel server URL the agent should connect to.
	Endpoint string
	// Fingerprint is the TLS fingerprint of the tunnel server, used
	// by the agent to verify server identity without a CA.
	Fingerprint string
	// Auth is the base64-encoded "user:password" string used for
	// tunnel authentication.
	Auth string
	// Token is the one-time token assigned to the agent during
	// registration, used to authenticate with the tunnel server.
	Token string
	// ProxyToken is the token the server must present on every
	// proxied request so the agent can verify the request origin.
	ProxyToken string
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
// the tunnel provider, and returns the tunnel endpoint, the
// authentication token, and the proxy token.
func (uc *FleetUseCase) RegisterCluster(cluster, agentID string) (Registration, error) {
	token, err := uc.generateToken()
	if err != nil {
		return Registration{}, err
	}
	proxyToken, err := uc.generateToken()
	if err != nil {
		return Registration{}, err
	}
	endpoint, err := uc.tunnel.RegisterCluster(cluster, agentID, token, proxyToken)
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		Endpoint:    endpoint,
		Fingerprint: uc.tunnel.Fingerprint(),
		Token:       token,
		ProxyToken:  proxyToken,
	}, nil
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
