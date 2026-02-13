package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kube-openapi/pkg/validation/spec"

	pb "github.com/otterscale/otterscale-agent/api/resource/v1"
	"github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/core"
)

// ResourceService implements the Resource gRPC service. It proxies
// Kubernetes CRUD and watch operations through the tunnel, translating
// between protobuf and unstructured Kubernetes objects.
type ResourceService struct {
	pbconnect.UnimplementedResourceServiceHandler

	resource *core.ResourceUseCase
}

// NewResourceService returns a ResourceService backed by the given
// use-case.
func NewResourceService(resource *core.ResourceUseCase) *ResourceService {
	return &ResourceService{
		resource: resource,
	}
}

var _ pbconnect.ResourceServiceHandler = (*ResourceService)(nil)

// ---------------------------------------------------------------------------
// Discovery / Schema
// ---------------------------------------------------------------------------

// Discovery returns the full list of API resources available on the
// target cluster.
func (s *ResourceService) Discovery(ctx context.Context, req *pb.DiscoveryRequest) (*pb.DiscoveryResponse, error) {
	apiResources, err := s.resource.ServerResources(ctx, req.GetCluster())
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}

	pbAPIResources, err := s.toProtoAPIResources(apiResources)
	if err != nil {
		return nil, err
	}

	resp := &pb.DiscoveryResponse{}
	resp.SetApiResources(pbAPIResources)
	return resp, nil
}

// Schema returns the OpenAPI schema for the given GVK, serialised as
// a protobuf Struct.
func (s *ResourceService) Schema(ctx context.Context, req *pb.SchemaRequest) (*structpb.Struct, error) {
	resolved, err := s.resource.ResolveSchema(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetKind(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}
	return s.toProtoStructFromJSONSchema(resolved)
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// List returns a paged list of resources matching the request filters.
func (s *ResourceService) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	resources, err := s.resource.ListResources(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetLabelSelector(),
		req.GetFieldSelector(),
		req.GetLimit(),
		req.GetContinue(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}

	pbResources, err := s.toProtoResources(resources.Items)
	if err != nil {
		return nil, err
	}

	resp := &pb.ListResponse{}
	resp.SetResourceVersion(resources.GetResourceVersion())
	resp.SetContinue(resources.GetContinue())
	resp.SetRemainingItemCount(deref(resources.GetRemainingItemCount(), 0))
	resp.SetItems(pbResources)
	return resp, nil
}

// Get returns a single resource by name.
func (s *ResourceService) Get(ctx context.Context, req *pb.GetRequest) (*pb.Resource, error) {
	resource, err := s.resource.GetResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetName(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}
	return s.toProtoResource(resource.Object)
}

// Create creates a new resource from the YAML manifest in the request.
func (s *ResourceService) Create(ctx context.Context, req *pb.CreateRequest) (*pb.Resource, error) {
	resource, err := s.resource.CreateResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetManifest(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}
	return s.toProtoResource(resource.Object)
}

// Apply performs a server-side apply for the given resource.
func (s *ResourceService) Apply(ctx context.Context, req *pb.ApplyRequest) (*pb.Resource, error) {
	resource, err := s.resource.ApplyResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetName(),
		req.GetManifest(),
		req.GetForce(),
		req.GetFieldManager(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}
	return s.toProtoResource(resource.Object)
}

// Delete removes the named resource. An optional grace period may be
// specified in the request.
func (s *ResourceService) Delete(ctx context.Context, req *pb.DeleteRequest) (*emptypb.Empty, error) {
	var gracePeriod *int64
	if req.HasGracePeriodSeconds() {
		v := req.GetGracePeriodSeconds()
		gracePeriod = &v
	}

	if err := s.resource.DeleteResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetName(),
		gracePeriod,
	); err != nil {
		return nil, k8sErrorToConnectError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Describe
// ---------------------------------------------------------------------------

// Describe returns a resource together with its related Kubernetes
// events, equivalent to `kubectl describe`.
func (s *ResourceService) Describe(ctx context.Context, req *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	obj, events, err := s.resource.DescribeResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetName(),
	)
	if err != nil {
		return nil, k8sErrorToConnectError(err)
	}

	pbResource, err := s.toProtoResource(obj.Object)
	if err != nil {
		return nil, err
	}

	pbEvents, err := s.toProtoResources(events.Items)
	if err != nil {
		return nil, err
	}

	resp := &pb.DescribeResponse{}
	resp.SetResource(pbResource)
	resp.SetEvents(pbEvents)
	return resp, nil
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

// Watch opens a server-streaming RPC that forwards Kubernetes watch
// events to the client. The stream ends when the client cancels the
// context or the upstream watcher closes.
func (s *ResourceService) Watch(ctx context.Context, req *pb.WatchRequest, stream *connect.ServerStream[pb.WatchEvent]) error {
	watcher, err := s.resource.WatchResource(
		ctx,
		req.GetCluster(),
		req.GetGroup(),
		req.GetVersion(),
		req.GetResource(),
		req.GetNamespace(),
		req.GetLabelSelector(),
		req.GetFieldSelector(),
		req.GetResourceVersion(),
	)
	if err != nil {
		return k8sErrorToConnectError(err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.ResultChan():
			if !ok {
				return k8sErrorToConnectError(apierrors.NewServiceUnavailable("watch closed"))
			}

			msg, ok := s.processEvent(event)
			if !ok {
				continue
			}

			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// processEvent converts a single Kubernetes watch.Event into a
// protobuf WatchEvent. Returns false if the event should be skipped
// (e.g. unexpected type).
func (s *ResourceService) processEvent(event watch.Event) (*pb.WatchEvent, bool) {
	switch event.Type {
	case watch.Added, watch.Modified, watch.Deleted:
		obj, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			slog.Warn("watch: unexpected object type", "eventType", event.Type)
			return nil, false
		}

		resource, err := s.toProtoResource(obj.Object)
		if err != nil {
			slog.Warn("watch: failed to convert resource to proto", "eventType", event.Type, "error", err)
			return nil, false
		}

		ret := &pb.WatchEvent{}
		ret.SetType(s.toProtoWatchEventType(event.Type))
		ret.SetResource(resource)
		return ret, true

	case watch.Bookmark:
		metadata, err := meta.Accessor(event.Object)
		if err != nil {
			slog.Warn("watch: failed to access bookmark metadata", "error", err)
			return nil, false
		}

		ret := &pb.WatchEvent{}
		ret.SetType(pb.WatchEvent_TYPE_BOOKMARK)
		ret.SetResourceVersion(metadata.GetResourceVersion())
		return ret, true

	case watch.Error:
		ret := &pb.WatchEvent{}
		ret.SetType(pb.WatchEvent_TYPE_ERROR)
		return ret, true

	default:
		slog.Warn("watch: unknown event type", "eventType", event.Type)
		return nil, false
	}
}

// toProtoAPIResources flattens the Kubernetes APIResourceList slice
// into a single []*pb.APIResource list, embedding the parsed
// group/version into each entry.
func (s *ResourceService) toProtoAPIResources(list []*metav1.APIResourceList) ([]*pb.APIResource, error) {
	ret := []*pb.APIResource{}

	for i := range list {
		gv, err := schema.ParseGroupVersion(list[i].GroupVersion)
		if err != nil {
			return nil, err
		}

		for j := range list[i].APIResources {
			ret = append(ret, s.toProtoAPIResource(gv, &list[i].APIResources[j]))
		}
	}

	return ret, nil
}

// toProtoAPIResource converts a single Kubernetes APIResource into its
// protobuf representation.
func (s *ResourceService) toProtoAPIResource(gv schema.GroupVersion, r *metav1.APIResource) *pb.APIResource {
	ret := &pb.APIResource{}
	ret.SetGroup(gv.Group)
	ret.SetVersion(gv.Version)
	ret.SetResource(r.Name)
	ret.SetKind(r.Kind)
	ret.SetNamespaced(r.Namespaced)
	ret.SetVerbs(r.Verbs)
	ret.SetShortNames(r.ShortNames)
	return ret
}

// toProtoStructFromJSONSchema serialises an OpenAPI spec.Schema to
// JSON and re-parses it into a protobuf Struct so it can be returned
// as a generic structured response.
func (s *ResourceService) toProtoStructFromJSONSchema(js *spec.Schema) (*structpb.Struct, error) {
	jsBytes, err := json.Marshal(js)
	if err != nil {
		return nil, err
	}

	ret := &structpb.Struct{}
	if err := protojson.Unmarshal(jsBytes, ret); err != nil {
		return nil, err
	}

	return ret, nil
}

// toProtoResources converts a slice of Unstructured objects into
// protobuf Resource messages.
func (s *ResourceService) toProtoResources(list []unstructured.Unstructured) ([]*pb.Resource, error) {
	ret := []*pb.Resource{}

	for i := range list {
		res, err := s.toProtoResource(list[i].Object)
		if err != nil {
			return nil, err
		}

		ret = append(ret, res)
	}

	return ret, nil
}

// toProtoResource wraps a raw Kubernetes object map in a protobuf
// Resource message.
func (s *ResourceService) toProtoResource(obj map[string]any) (*pb.Resource, error) {
	object, err := structpb.NewStruct(obj)
	if err != nil {
		return nil, err
	}

	ret := &pb.Resource{}
	ret.SetObject(object)
	return ret, nil
}

// toProtoWatchEventType maps a Kubernetes watch.EventType to the
// protobuf WatchEvent_Type enum.
func (s *ResourceService) toProtoWatchEventType(t watch.EventType) pb.WatchEvent_Type {
	switch t {
	case watch.Added:
		return pb.WatchEvent_TYPE_ADDED
	case watch.Modified:
		return pb.WatchEvent_TYPE_MODIFIED
	case watch.Deleted:
		return pb.WatchEvent_TYPE_DELETED
	case watch.Bookmark:
		return pb.WatchEvent_TYPE_BOOKMARK
	case watch.Error:
		return pb.WatchEvent_TYPE_ERROR
	default:
		return pb.WatchEvent_TYPE_UNSPECIFIED
	}
}

// deref returns the value pointed to by ptr, or def if ptr is nil.
func deref[T any](ptr *T, def T) T {
	if ptr != nil {
		return *ptr
	}
	return def
}
