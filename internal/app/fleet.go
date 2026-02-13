package app

import (
	"context"

	pb "github.com/otterscale/otterscale-agent/api/fleet/v1"
	"github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
)

type FleetService struct {
	pbconnect.UnimplementedFleetServiceHandler

	fleet *core.FleetUseCase
}

func NewFleetService(fleet *core.FleetUseCase) *FleetService {
	return &FleetService{
		fleet: fleet,
	}
}

var _ pbconnect.FleetServiceHandler = (*FleetService)(nil)

func (s *FleetService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	host, token, err := s.fleet.RegisterCluster(req.GetCluster(), req.GetAgentId())
	if err != nil {
		return nil, err
	}

	resp := &pb.RegisterResponse{}
	resp.SetTunnelHost(host)
	resp.SetFingerprint(s.fleet.Fingerprint())
	resp.SetToken(token)
	return resp, nil
}
