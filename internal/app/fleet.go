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
	token, port, err := s.fleet.RegisterCluster(req.GetCluster(), req.GetAgentId())
	if err != nil {
		return nil, err
	}

	resp := &pb.RegisterResponse{}
	resp.SetFingerprint(s.fleet.Fingerprint())
	resp.SetToken(token)
	resp.SetTunnelPort(int32(port))
	return resp, nil
}
