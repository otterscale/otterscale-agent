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

type fleetRegistrar struct {
	client *http.Client
}

func NewFleetRegistrar() core.TunnelConsumer {
	return &fleetRegistrar{
		client: http.DefaultClient,
	}
}

var _ core.TunnelConsumer = (*fleetRegistrar)(nil)

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
