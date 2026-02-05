package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/otterscale/otterscale-agent/api/resource/v1"
	"github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	chiseltunnel "github.com/otterscale/otterscale-agent/internal/chisel"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
	"github.com/otterscale/otterscale-agent/internal/leader"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// ResourceProxy is the server-side ResourceService implementation which forwards
// requests to the appropriate agent via chisel reverse tunnels.
type ResourceProxy struct {
	pbconnect.UnimplementedResourceServiceHandler

	conf    *config.Config
	tunnels *chiseltunnel.TunnelService
	leader  *leader.Elector

	httpClient *http.Client

	mu      sync.Mutex
	clients map[string]pbconnect.ResourceServiceClient // cluster -> client
}

func NewResourceProxy(conf *config.Config, tunnels *chiseltunnel.TunnelService, leaderElector *leader.Elector) *ResourceProxy {
	return &ResourceProxy{
		conf:    conf,
		tunnels: tunnels,
		leader:  leaderElector,
		httpClient: &http.Client{
			// NOTE: no global timeout; watch streams can be long-lived.
			Timeout: 0,
		},
		clients: map[string]pbconnect.ResourceServiceClient{},
	}
}

func (p *ResourceProxy) upstream(cluster string) (pbconnect.ResourceServiceClient, error) {
	if p.leader != nil && !p.leader.IsLeader() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not leader"))
	}

	// Wait a bit for the tunnel port to become reachable.
	baseURL, err := p.tunnels.AgentBaseURL(cluster, 3*time.Second)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.clients[cluster]; ok {
		return c, nil
	}

	// Propagate the verified subject to agent via a trusted header.
	subjectHeaderInterceptor := impersonation.NewPropagateSubjectHeaderInterceptor()

	c := pbconnect.NewResourceServiceClient(
		p.httpClient,
		baseURL,
		connect.WithInterceptors(subjectHeaderInterceptor),
	)
	p.clients[cluster] = c
	return c, nil
}

func (p *ResourceProxy) Discovery(ctx context.Context, req *pb.DiscoveryRequest) (*pb.DiscoveryResponse, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Discovery(ctx, req)
}

func (p *ResourceProxy) Schema(ctx context.Context, req *pb.SchemaRequest) (*structpb.Struct, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Schema(ctx, req)
}

func (p *ResourceProxy) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.List(ctx, req)
}

func (p *ResourceProxy) Get(ctx context.Context, req *pb.GetRequest) (*pb.Resource, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Get(ctx, req)
}

func (p *ResourceProxy) Create(ctx context.Context, req *pb.CreateRequest) (*pb.Resource, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Create(ctx, req)
}

func (p *ResourceProxy) Apply(ctx context.Context, req *pb.ApplyRequest) (*pb.Resource, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Apply(ctx, req)
}

func (p *ResourceProxy) Delete(ctx context.Context, req *pb.DeleteRequest) (*emptypb.Empty, error) {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return c.Delete(ctx, req)
}

func (p *ResourceProxy) Watch(ctx context.Context, req *pb.WatchRequest, stream *connect.ServerStream[pb.WatchEvent]) error {
	c, err := p.upstream(req.GetCluster())
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}

	up, err := c.Watch(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = up.Close() }()

	for up.Receive() {
		if err := stream.Send(up.Msg()); err != nil {
			return err
		}
	}

	if err := up.Err(); err != nil {
		return err
	}

	// Propagate any trailers/headers? (Connect handles this internally for downstream)
	return nil
}

var _ pbconnect.ResourceServiceHandler = (*ResourceProxy)(nil)
