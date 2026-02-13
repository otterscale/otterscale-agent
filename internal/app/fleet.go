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
	resp.SetClusters(s.toProtoClusters(clusters))
	return resp, nil
}

// Register validates and signs the agent's CSR, allocates a tunnel
// endpoint, and returns the signed certificate together with the CA
// certificate for mTLS. The response includes the server version so
// agents can detect mismatches and self-update.
func (s *FleetService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	reg, err := s.fleet.RegisterCluster(req.GetCluster(), req.GetAgentId(), req.GetAgentVersion(), req.GetCsr())
	if err != nil {
		return nil, err
	}

	resp := &pb.RegisterResponse{}
	resp.SetEndpoint(reg.Endpoint)
	resp.SetCertificate(reg.Certificate)
	resp.SetCaCertificate(reg.CACertificate)
	resp.SetServerVersion(reg.ServerVersion)
	return resp, nil
}

// toProtoResources converts a slice of Unstructured objects into
// protobuf Resource messages.
func (s *FleetService) toProtoClusters(m map[string]core.Cluster) []*pb.Cluster {
	ret := []*pb.Cluster{}
	for name, cluster := range m {
		ret = append(ret, s.toProtoCluster(name, cluster))
	}
	return ret
}

// toProtoResource wraps a raw Kubernetes object map in a protobuf
// Resource message.
func (s *FleetService) toProtoCluster(name string, cluster core.Cluster) *pb.Cluster {
	ret := &pb.Cluster{}
	ret.SetName(name)
	ret.SetAgentVersion(cluster.AgentVersion)
	return ret
}
