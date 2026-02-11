package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Masterminds/semver/v3"
	"golang.org/x/sync/singleflight"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

type DiscoveryClient interface {
	LookupResource(ctx context.Context, cluster, group, version, resource string) (schema.GroupVersionResource, error)
	GetServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error)
	ResolveSchema(ctx context.Context, cluster, group, version, kind string) (*spec.Schema, error)
	GetServerVersion(ctx context.Context, cluster string) (*version.Info, error)
}

type ResourceRepo interface {
	List(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error)
	Get(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	Create(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	Apply(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, data []byte, force bool, fieldManager string) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, gracePeriodSeconds *int64) error
	Watch(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string, sendInitialEvents bool) (watch.Interface, error)
}

const schemaCacheTTL = 10 * time.Minute

type schemaCacheEntry struct {
	schema    *spec.Schema
	expiresAt time.Time
}

type ResourceUseCase struct {
	discovery DiscoveryClient
	resource  ResourceRepo

	schemaCache   sync.Map
	schemaFlights singleflight.Group
}

func NewResourceUseCase(discovery DiscoveryClient, resource ResourceRepo) *ResourceUseCase {
	return &ResourceUseCase{
		discovery: discovery,
		resource:  resource,
	}
}

func (uc *ResourceUseCase) GetServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error) {
	return uc.discovery.GetServerResources(ctx, cluster)
}

func (uc *ResourceUseCase) ResolveSchema(ctx context.Context, cluster, group, version, kind string) (*spec.Schema, error) {
	key := uc.schemaCacheKey(cluster, group, version, kind)

	if v, ok := uc.schemaCache.Load(key); ok {
		entry := v.(*schemaCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.schema, nil
		}
		uc.schemaCache.Delete(key)
	}

	v, err, _ := uc.schemaFlights.Do(key, func() (any, error) {
		schema, err := uc.discovery.ResolveSchema(ctx, cluster, group, version, kind)
		if err != nil {
			return nil, err
		}

		uc.schemaCache.Store(key, &schemaCacheEntry{
			schema:    schema,
			expiresAt: time.Now().Add(schemaCacheTTL),
		})

		return schema, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*spec.Schema), nil
}

func (uc *ResourceUseCase) ListResources(ctx context.Context, cluster, group, version, resource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	list, err := uc.resource.List(ctx, cluster, gvr, namespace, labelSelector, fieldSelector, limit, continueToken)
	if err != nil {
		return nil, err
	}

	for i := range list.Items {
		uc.cleanObject(&list.Items[i])
	}

	return list, nil
}

func (uc *ResourceUseCase) GetResource(ctx context.Context, cluster, group, version, resource, namespace, name string) (*unstructured.Unstructured, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	return uc.resource.Get(ctx, cluster, gvr, namespace, name)
}

func (uc *ResourceUseCase) CreateResource(ctx context.Context, cluster, group, version, resource, namespace string, manifest []byte) (*unstructured.Unstructured, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	obj, err := uc.fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	return uc.resource.Create(ctx, cluster, gvr, namespace, obj)
}

func (uc *ResourceUseCase) ApplyResource(ctx context.Context, cluster, group, version, resource, namespace, name string, manifest []byte, force bool, fieldManager string) (*unstructured.Unstructured, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	obj, err := uc.fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	return uc.resource.Apply(ctx, cluster, gvr, namespace, name, data, force, fieldManager)
}

func (uc *ResourceUseCase) DeleteResource(ctx context.Context, cluster, group, version, resource, namespace, name string, gracePeriodSeconds *int64) error {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return err
	}

	return uc.resource.Delete(ctx, cluster, gvr, namespace, name, gracePeriodSeconds)
}

func (uc *ResourceUseCase) WatchResource(ctx context.Context, cluster, group, version, resource, namespace, labelSelector, fieldSelector, resourceVersion string) (watch.Interface, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	watchListFeature, err := uc.watchListFeature(ctx, cluster)
	if err != nil {
		return nil, err
	}

	return uc.resource.Watch(ctx, cluster, gvr, namespace, labelSelector, fieldSelector, resourceVersion, watchListFeature)
}

func (uc *ResourceUseCase) schemaCacheKey(cluster, group, version, kind string) string {
	return strings.Join([]string{cluster, group, version, kind}, "/")
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

func (uc *ResourceUseCase) watchListFeature(ctx context.Context, cluster string) (bool, error) {
	version, err := uc.discovery.GetServerVersion(ctx, cluster)
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
