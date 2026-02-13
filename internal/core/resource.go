package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/sync/singleflight"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// DiscoveryClient abstracts Kubernetes API discovery so that the
// use-case layer can validate resources and fetch schemas without
// depending on a concrete client implementation.
type DiscoveryClient interface {
	// LookupResource validates that a group/version/resource triple
	// exists on the target cluster.
	LookupResource(ctx context.Context, cluster, group, version, resource string) (schema.GroupVersionResource, error)
	// ServerResources returns all API resources advertised by the cluster.
	ServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error)
	// ResolveSchema fetches the OpenAPI schema for a given GVK.
	ResolveSchema(ctx context.Context, cluster, group, version, kind string) (*spec.Schema, error)
	// ServerVersion returns the Kubernetes version of the cluster.
	ServerVersion(ctx context.Context, cluster string) (*version.Info, error)
}

// ResourceRepo abstracts Kubernetes resource CRUD and watch operations
// through the dynamic client. All methods accept a cluster name so
// that the underlying implementation can route requests through the
// correct tunnel.
type ResourceRepo interface {
	List(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error)
	Get(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	Create(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	Apply(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, data []byte, force bool, fieldManager string) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, gracePeriodSeconds *int64) error
	Watch(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string, sendInitialEvents bool) (watch.Interface, error)
}

// schemaCacheTTL controls how long resolved OpenAPI schemas are kept
// in memory before being re-fetched from the cluster.
const schemaCacheTTL = 10 * time.Minute

// minWatchListVersion is the minimum Kubernetes version that supports
// the WatchList streaming feature (beta, default-on since 1.34).
var minWatchListVersion = semver.MustParse("v1.34.0")

// schemaCacheEntry pairs a cached schema with its expiration time.
type schemaCacheEntry struct {
	schema    *spec.Schema
	expiresAt time.Time
}

// ResourceUseCase provides the application-level API for managing
// Kubernetes resources across multiple clusters. It validates GVRs
// via the DiscoveryClient, caches OpenAPI schemas with singleflight
// deduplication, and strips noisy metadata (managedFields,
// last-applied-configuration) from list results.
type ResourceUseCase struct {
	discovery DiscoveryClient
	resource  ResourceRepo

	mu            sync.RWMutex
	schemaCache   map[string]*schemaCacheEntry
	schemaFlights singleflight.Group
}

// NewResourceUseCase returns a ResourceUseCase wired to the given
// discovery and resource backends.
func NewResourceUseCase(discovery DiscoveryClient, resource ResourceRepo) *ResourceUseCase {
	return &ResourceUseCase{
		discovery:   discovery,
		resource:    resource,
		schemaCache: make(map[string]*schemaCacheEntry),
	}
}

// ServerResources returns all API resource lists from the target cluster.
func (uc *ResourceUseCase) ServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error) {
	return uc.discovery.ServerResources(ctx, cluster)
}

// ResolveSchema fetches the OpenAPI schema for the given GVK. Results
// are cached for schemaCacheTTL and concurrent requests for the same
// key are deduplicated via singleflight.
func (uc *ResourceUseCase) ResolveSchema(ctx context.Context, cluster, group, version, kind string) (*spec.Schema, error) {
	key := uc.schemaCacheKey(cluster, group, version, kind)

	uc.mu.RLock()
	entry, ok := uc.schemaCache[key]
	uc.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.schema, nil
	}

	v, err, _ := uc.schemaFlights.Do(key, func() (any, error) {
		resolved, err := uc.discovery.ResolveSchema(ctx, cluster, group, version, kind)
		if err != nil {
			return nil, err
		}

		uc.mu.Lock()
		uc.schemaCache[key] = &schemaCacheEntry{
			schema:    resolved,
			expiresAt: time.Now().Add(schemaCacheTTL),
		}
		uc.mu.Unlock()

		return resolved, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*spec.Schema), nil
}

// ListResources validates the GVR, fetches a paged resource list, and
// strips noisy metadata from each item before returning.
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

// GetResource validates the GVR and fetches a single resource.
func (uc *ResourceUseCase) GetResource(ctx context.Context, cluster, group, version, resource, namespace, name string) (*unstructured.Unstructured, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	return uc.resource.Get(ctx, cluster, gvr, namespace, name)
}

// CreateResource validates the GVR, decodes the YAML manifest, and
// creates the resource on the target cluster.
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

// ApplyResource validates the GVR, decodes the YAML manifest, and
// performs a server-side apply on the target cluster.
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

// DeleteResource validates the GVR and deletes the named resource.
func (uc *ResourceUseCase) DeleteResource(ctx context.Context, cluster, group, version, resource, namespace, name string, gracePeriodSeconds *int64) error {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return err
	}

	return uc.resource.Delete(ctx, cluster, gvr, namespace, name, gracePeriodSeconds)
}

// WatchResource validates the GVR and opens a long-lived watch stream.
// If the cluster supports the WatchList feature (Kubernetes >= 1.34),
// initial events are streamed before switching to change notifications.
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

// schemaCacheKey builds a cache key from the cluster/group/version/kind tuple.
func (uc *ResourceUseCase) schemaCacheKey(cluster, group, version, kind string) string {
	return strings.Join([]string{cluster, group, version, kind}, "/")
}

// fromYAML decodes a YAML manifest into an Unstructured object.
// Returns a BadRequest API error if the manifest is invalid.
func (uc *ResourceUseCase) fromYAML(manifest []byte) (*unstructured.Unstructured, error) {
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj := &unstructured.Unstructured{}

	if _, _, err := dec.Decode(manifest, nil, obj); err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("invalid manifest: %s", err))
	}

	return obj, nil
}

// cleanObject strips noisy metadata that clutters list output:
//   - metadata.managedFields (server-side apply bookkeeping)
//   - the kubectl.kubernetes.io/last-applied-configuration annotation
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

// watchListFeature reports whether the target cluster supports the
// WatchList streaming feature (beta, default-on since Kubernetes 1.34).
// See https://kubernetes.io/docs/reference/using-api/api-concepts/#streaming-lists
func (uc *ResourceUseCase) watchListFeature(ctx context.Context, cluster string) (bool, error) {
	info, err := uc.discovery.ServerVersion(ctx, cluster)
	if err != nil {
		return false, err
	}

	kubeVersion, err := semver.NewVersion(info.String())
	if err != nil {
		return false, err
	}

	return kubeVersion.GreaterThanEqual(minWatchListVersion), nil
}
