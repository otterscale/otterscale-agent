package kubernetes

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/otterscale/otterscale-agent/internal/core"
)

type discoveryRepo struct {
	kubernetes *Kubernetes
}

func NewDiscoveryRepo(kubernetes *Kubernetes) core.DiscoveryRepo {
	return &discoveryRepo{
		kubernetes: kubernetes,
	}
}

var _ core.DiscoveryRepo = (*discoveryRepo)(nil)

func (r *discoveryRepo) List(cluster string) ([]*metav1.APIResourceList, error) {
	client, err := r.kubernetes.discovery(cluster)
	if err != nil {
		return nil, err
	}

	_, resources, err := client.ServerGroupsAndResources()
	return resources, err
}

func (r *discoveryRepo) Schema(cluster, group, version, kind string) (*spec.Schema, error) {
	client, err := r.kubernetes.discovery(cluster)
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

func (r *discoveryRepo) Validate(cluster, group, version, res string) (core.ClusterGroupVersionResource, error) {
	client, err := r.kubernetes.discovery(cluster)
	if err != nil {
		return core.ClusterGroupVersionResource{}, err
	}

	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: res,
	}

	resources, err := client.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return core.ClusterGroupVersionResource{}, err
	}

	for i := range resources.APIResources {
		if resources.APIResources[i].Name == gvr.Resource {
			return core.ClusterGroupVersionResource{
				Cluster:              cluster,
				GroupVersionResource: gvr,
			}, nil
		}
	}
	return core.ClusterGroupVersionResource{}, apierrors.NewBadRequest(fmt.Sprintf("unable to recognize resource %q in %s", res, schema.GroupVersion{Group: group, Version: version}))
}

func (r *discoveryRepo) Version(cluster string) (*version.Info, error) {
	client, err := r.kubernetes.discovery(cluster)
	if err != nil {
		return nil, err
	}
	return client.ServerVersion()
}
