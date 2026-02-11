package kubernetes

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	"k8s.io/client-go/discovery"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/otterscale/otterscale-agent/internal/core"
)

type discoveryClient struct {
	kubernetes *Kubernetes
}

func NewDiscoveryClient(kubernetes *Kubernetes) core.DiscoveryClient {
	return &discoveryClient{
		kubernetes: kubernetes,
	}
}

var _ core.DiscoveryClient = (*discoveryClient)(nil)

func (d *discoveryClient) LookupResource(ctx context.Context, cluster, group, version, resource string) (schema.GroupVersionResource, error) {
	client, err := d.client(ctx, cluster)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	resources, err := client.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	for i := range resources.APIResources {
		if resources.APIResources[i].Name == gvr.Resource {
			return gvr, nil
		}
	}
	return schema.GroupVersionResource{}, apierrors.NewBadRequest(fmt.Sprintf("unable to recognize resource %s", gvr))
}

func (d *discoveryClient) GetServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error) {
	client, err := d.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	_, resources, err := client.ServerGroupsAndResources()
	return resources, err
}

func (d *discoveryClient) ResolveSchema(ctx context.Context, cluster, group, version, kind string) (*spec.Schema, error) {
	client, err := d.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	resolver := &resolver.ClientDiscoveryResolver{
		Discovery: client,
	}
	gvk := schema.GroupVersionKind{
		Group:   group,
		Version: version,
		Kind:    kind,
	}
	return resolver.ResolveSchema(gvk)
}

func (d *discoveryClient) GetServerVersion(ctx context.Context, cluster string) (*version.Info, error) {
	client, err := d.client(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return client.ServerVersion()
}

func (d *discoveryClient) client(ctx context.Context, cluster string) (*discovery.DiscoveryClient, error) {
	config, err := d.kubernetes.impersonationConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return discovery.NewDiscoveryClientForConfig(config)
}
