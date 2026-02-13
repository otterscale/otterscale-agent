// Package app implements the ConnectRPC service handlers that form
// the server's public API. Each handler translates between protobuf
// messages and the domain use-cases defined in package core.
package app

import (
	"context"

	pb "github.com/otterscale/otterscale-agent/api/fleet/v1"
	"github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
)

// FleetService implements the Fleet gRPC service. It handles cluster
// listing and agent registration.
type FleetService struct {
	pbconnect.UnimplementedFleetServiceHandler

	fleet *core.FleetUseCase
}

// NewFleetService returns a FleetService backed by the given use-case.
func NewFleetService(fleet *core.FleetUseCase) *FleetService {
	return &FleetService{
		fleet: fleet,
	}
}

var _ pbconnect.FleetServiceHandler = (*FleetService)(nil)

// ListClusters returns the names of all clusters that have a
// registered agent.
func (s *FleetService) ListClusters(ctx context.Context, req *pb.ListClustersRequest) (*pb.ListClustersResponse, error) {
	clusters := s.fleet.ListClusters()

	resp := &pb.ListClustersResponse{}
	resp.SetClusters(clusters)
	return resp, nil
}

// Register validates and signs the agent's CSR, allocates a tunnel
// endpoint, and returns the signed certificate together with the CA
// certificate for mTLS.
func (s *FleetService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	reg, err := s.fleet.RegisterCluster(req.GetCluster(), req.GetAgentId(), req.GetCsr())
	if err != nil {
		return nil, err
	}

	resp := &pb.RegisterResponse{}
	resp.SetEndpoint(reg.Endpoint)
	resp.SetCertificate(reg.Certificate)
	resp.SetCaCertificate(reg.CACertificate)
	return resp, nil
}
