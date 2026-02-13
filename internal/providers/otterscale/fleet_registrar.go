// Package otterscale implements core.TunnelConsumer by calling the
// otterscale fleet gRPC service (via ConnectRPC) to register an agent
// and obtain mTLS tunnel credentials.
package otterscale

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	pb "github.com/otterscale/otterscale-agent/api/fleet/v1"
	"github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/pki"
)

// fleetRegistrar implements core.TunnelConsumer by generating a CSR,
// calling the remote fleet service to have it signed, and returning
// the resulting mTLS materials.
type fleetRegistrar struct {
	agentID    string
	client     *http.Client
	csrPEM     []byte
	privateKey []byte // PEM-encoded ECDSA private key
}

// NewFleetRegistrar returns a TunnelConsumer that registers agents
// against the otterscale fleet API using CSR-based mTLS enrolment.
// A fresh ECDSA P-256 key pair and CSR are generated at construction
// time and reused across registrations.
func NewFleetRegistrar() (core.TunnelConsumer, error) {
	agentID, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %w", err)
	}

	key, keyPEM, err := pki.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	csrPEM, err := pki.GenerateCSR(key, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CSR: %w", err)
	}

	return &fleetRegistrar{
		agentID: agentID,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		csrPEM:     csrPEM,
		privateKey: keyPEM,
	}, nil
}

var _ core.TunnelConsumer = (*fleetRegistrar)(nil)

// Register calls the fleet service's Register RPC with the agent's
// CSR. The server signs the CSR with its internal CA and returns the
// signed certificate, CA certificate, and tunnel endpoint.
func (f *fleetRegistrar) Register(ctx context.Context, serverURL, cluster string) (core.Registration, error) {
	client := pbconnect.NewFleetServiceClient(f.client, serverURL)
	req := &pb.RegisterRequest{}
	req.SetCluster(cluster)
	req.SetAgentId(f.agentID)
	req.SetCsr(f.csrPEM)

	resp, err := client.Register(ctx, req)
	if err != nil {
		return core.Registration{}, err
	}

	return core.Registration{
		Endpoint:      resp.GetEndpoint(),
		Certificate:   resp.GetCertificate(),
		CACertificate: resp.GetCaCertificate(),
		AgentID:       f.agentID,
	}, nil
}

// PrivateKeyPEM returns the PEM-encoded private key that corresponds
// to the CSR sent during registration.
func (f *fleetRegistrar) PrivateKeyPEM() []byte {
	return f.privateKey
}
