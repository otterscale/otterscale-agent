// Package core defines the domain interfaces and use-case logic for
// the otterscale agent. Infrastructure adapters (chisel, kubernetes,
// otterscale) implement the interfaces declared here.
package core

import "context"

// TunnelProvider is the server-side abstraction for managing reverse
// tunnels. It allocates unique endpoints per cluster, signs agent
// CSRs, and provisions tunnel users for each connecting agent.
type TunnelProvider interface {
	// CACertPEM returns the PEM-encoded CA certificate so that
	// agents can verify the tunnel server and the server can
	// configure mTLS.
	CACertPEM() []byte
	// ListClusters returns the names of all registered clusters.
	ListClusters() []string
	// RegisterCluster validates and signs the agent's CSR, creates
	// a tunnel user, and returns the allocated endpoint together
	// with the PEM-encoded signed certificate.
	RegisterCluster(cluster, agentID string, csrPEM []byte) (endpoint string, certPEM []byte, err error)
	// ResolveAddress returns the HTTP base URL for the given cluster.
	ResolveAddress(cluster string) (string, error)
}

// TunnelConsumer is the agent-side abstraction for registering with
// the fleet server and obtaining tunnel credentials via CSR/mTLS.
type TunnelConsumer interface {
	// Register calls the fleet API with a CSR and returns the
	// signed certificate, CA certificate, and tunnel endpoint.
	Register(ctx context.Context, serverURL, cluster string) (Registration, error)
	// PrivateKeyPEM returns the PEM-encoded private key that
	// corresponds to the CSR sent during registration.
	PrivateKeyPEM() []byte
}

// Registration holds the credentials and connection details returned
// by the fleet server after a successful CSR-based registration.
type Registration struct {
	// Endpoint is the tunnel endpoint the agent should connect to.
	Endpoint string
	// Certificate is the PEM-encoded X.509 certificate signed by
	// the server's CA, used for mTLS client authentication.
	Certificate []byte
	// CACertificate is the PEM-encoded CA certificate used to
	// verify the tunnel server's identity.
	CACertificate []byte
	// AgentID is the identifier of the agent that registered. It is
	// set by the TunnelConsumer so that callers can derive auth
	// credentials without re-querying the hostname.
	AgentID string
}

// FleetUseCase orchestrates cluster registration on the server side.
// It delegates CSR signing and tunnel setup to the TunnelProvider.
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

// RegisterCluster forwards the agent's CSR to the tunnel provider for
// signing, and returns the signed certificate, CA certificate, and
// tunnel endpoint.
func (uc *FleetUseCase) RegisterCluster(cluster, agentID string, csrPEM []byte) (Registration, error) {
	endpoint, certPEM, err := uc.tunnel.RegisterCluster(cluster, agentID, csrPEM)
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		Endpoint:      endpoint,
		Certificate:   certPEM,
		CACertificate: uc.tunnel.CACertPEM(),
	}, nil
}
