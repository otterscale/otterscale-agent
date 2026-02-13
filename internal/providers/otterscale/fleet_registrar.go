// Package otterscale implements core.TunnelConsumer by calling the
// otterscale fleet gRPC service (via ConnectRPC) to register an agent
// and obtain tunnel credentials.
package otterscale

import (
	"context"
	"fmt"
	"net/http"
	"os"

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
		client: http.DefaultClient,
	}
}

var _ core.TunnelConsumer = (*fleetRegistrar)(nil)

// Register calls the fleet service's Register RPC with the local
// hostname as the agent ID. It returns the tunnel endpoint, the
// server's TLS fingerprint, and an auth string in "agentID:token"
// format that the chisel client uses for authentication.
func (f *fleetRegistrar) Register(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error) {
	agentID, err := os.Hostname()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get hostname: %w", err)
	}

	client := pbconnect.NewFleetServiceClient(f.client, serverURL)
	req := &pb.RegisterRequest{}
	req.SetCluster(cluster)
	req.SetAgentId(agentID)

	resp, err := client.Register(ctx, req)
	if err != nil {
		return "", "", "", err
	}

	return resp.GetEndpoint(), resp.GetFingerprint(), fmt.Sprintf("%s:%s", agentID, resp.GetToken()), nil
}
