// Package otterscale implements core.TunnelConsumer by calling the
// otterscale fleet gRPC service (via ConnectRPC) to register an agent
// and obtain tunnel credentials.
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
)

// fleetRegistrar implements core.TunnelConsumer by calling the remote
// fleet service to register this agent and receive a one-time tunnel
// token.
type fleetRegistrar struct {
	client *http.Client
}

// NewFleetRegistrar returns a TunnelConsumer that registers agents
// against the otterscale fleet API.
func NewFleetRegistrar() core.TunnelConsumer {
	return &fleetRegistrar{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

var _ core.TunnelConsumer = (*fleetRegistrar)(nil)

// Register calls the fleet service's Register RPC with the local
// hostname as the agent ID. It returns the tunnel endpoint, the
// server's TLS fingerprint, an auth string in "agentID:token" format
// that the chisel client uses for authentication, and a proxy token
// for HTTP-level authentication of tunnelled requests.
func (f *fleetRegistrar) Register(ctx context.Context, serverURL, cluster string) (core.Registration, error) {
	agentID, err := os.Hostname()
	if err != nil {
		return core.Registration{}, fmt.Errorf("failed to get hostname: %w", err)
	}

	client := pbconnect.NewFleetServiceClient(f.client, serverURL)
	req := &pb.RegisterRequest{}
	req.SetCluster(cluster)
	req.SetAgentId(agentID)

	resp, err := client.Register(ctx, req)
	if err != nil {
		return core.Registration{}, err
	}

	return core.Registration{
		Endpoint:    resp.GetEndpoint(),
		Fingerprint: resp.GetFingerprint(),
		Auth:        fmt.Sprintf("%s:%s", agentID, resp.GetToken()),
		ProxyToken:  resp.GetProxyToken(),
	}, nil
}
