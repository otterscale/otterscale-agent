package core

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Masterminds/semver/v3"
	"golang.org/x/sync/singleflight"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

//nolint:revive // allows this exported struct name.
type ResourceRepo interface {
	List(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error)
	Get(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	Create(ctx context.Context, cgvr ClusterGroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	Apply(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string, data []byte, force bool, fieldManager string) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string, gracePeriodSeconds *int64) error
	Watch(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string, sendInitialEvents bool) (watch.Interface, error)
}

type ResourceUseCase struct {
	discovery DiscoveryRepo
	resource  ResourceRepo

	schemaCache   sync.Map
	schemaFlights singleflight.Group
}

func NewResourceUseCase(discovery DiscoveryRepo, resource ResourceRepo) *ResourceUseCase {
	return &ResourceUseCase{
		discovery: discovery,
		resource:  resource,
	}
}

func (uc *ResourceUseCase) Validate(cluster, group, version, resource string) (ClusterGroupVersionResource, error) {
	return uc.discovery.Validate(cluster, group, version, resource)
}

func (uc *ResourceUseCase) ListAPIResources(cluster string) ([]*metav1.APIResourceList, error) {
	return uc.discovery.List(cluster)
}

func (uc *ResourceUseCase) GetSchema(cluster, group, version, kind string) (*spec.Schema, error) {
	key := uc.schemaCacheKey(cluster, group, version, kind)

	if v, ok := uc.schemaCache.Load(key); ok {
		return v.(*spec.Schema), nil
	}

	v, err, _ := uc.schemaFlights.Do(key, func() (any, error) {
		schema, err := uc.discovery.Schema(cluster, group, version, kind)
		if err != nil {
			return nil, err
		}

		uc.schemaCache.Store(key, schema)

		return schema, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*spec.Schema), nil
}

func (uc *ResourceUseCase) ListResources(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error) {
	list, err := uc.resource.List(ctx, cgvr, namespace, labelSelector, fieldSelector, limit, continueToken)
	if err != nil {
		return nil, err
	}

	for i := range list.Items {
		uc.cleanObject(&list.Items[i])
	}

	return list, nil
}

func (uc *ResourceUseCase) GetResource(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return uc.resource.Get(ctx, cgvr, namespace, name)
}

func (uc *ResourceUseCase) CreateResource(ctx context.Context, cgvr ClusterGroupVersionResource, namespace string, manifest []byte) (*unstructured.Unstructured, error) {
	obj, err := uc.fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	return uc.resource.Create(ctx, cgvr, namespace, obj)
}

func (uc *ResourceUseCase) ApplyResource(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string, manifest []byte, force bool, fieldManager string) (*unstructured.Unstructured, error) {
	obj, err := uc.fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	return uc.resource.Apply(ctx, cgvr, namespace, name, data, force, fieldManager)
}

func (uc *ResourceUseCase) DeleteResource(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, name string, gracePeriodSeconds int64) error {
	return uc.resource.Delete(ctx, cgvr, namespace, name, &gracePeriodSeconds)
}

func (uc *ResourceUseCase) WatchResource(ctx context.Context, cgvr ClusterGroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string) (watch.Interface, error) {
	watchListFeature, err := uc.watchListFeature(cgvr.Cluster)
	if err != nil {
		return nil, err
	}

	return uc.resource.Watch(ctx, cgvr, namespace, labelSelector, fieldSelector, resourceVersion, watchListFeature)
}

func (uc *ResourceUseCase) fromYAML(manifest []byte) (*unstructured.Unstructured, error) {
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj := &unstructured.Unstructured{}

	if _, _, err := dec.Decode(manifest, nil, obj); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid manifest: %v", err))
	}

	return obj, nil
}

func (uc *ResourceUseCase) cleanObject(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")

	annotations := obj.GetAnnotations()
	if len(annotations) > 0 {
		if _, exists := annotations["kubectl.kubernetes.io/last-applied-configuration"]; exists {
			delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")

			if len(annotations) == 0 {
				unstructured.RemoveNestedField(obj.Object, "metadata", "annotations")
			} else {
				obj.SetAnnotations(annotations)
			}
		}
	}
}

func (uc *ResourceUseCase) watchListFeature(cluster string) (bool, error) {
	version, err := uc.discovery.Version(cluster)
	if err != nil {
		return false, err
	}

	kubeVersion, err := semver.NewVersion(version.String())
	if err != nil {
		return false, err
	}

	// https://kubernetes.io/docs/reference/using-api/api-concepts/#streaming-lists
	// v1.34 beta default on
	watchListVersion, err := semver.NewVersion("v1.34.0")
	if err != nil {
		return false, err
	}

	return kubeVersion.GreaterThanEqual(watchListVersion), nil
}

func (uc *ResourceUseCase) schemaCacheKey(cluster, group, version, kind string) string {
	return strings.Join([]string{cluster, group, version, kind}, "/")
}
