package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

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
	// List returns a paged list of resources matching the given options.
	List(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace string, opts ListOptions,
	) (*unstructured.UnstructuredList, error)

	// Get returns a single resource by name.
	Get(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace, name string,
	) (*unstructured.Unstructured, error)

	// Create creates a new resource from the given object.
	Create(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace string, obj *unstructured.Unstructured,
	) (*unstructured.Unstructured, error)

	// Apply performs a server-side apply (PATCH with ApplyPatchType) for
	// the given resource.
	Apply(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace, name string, data []byte, opts ApplyOptions,
	) (*unstructured.Unstructured, error)

	// Delete removes a resource.
	Delete(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace, name string, opts DeleteOptions,
	) error

	// Watch opens a long-lived watch stream for resources matching the
	// given options.
	Watch(ctx context.Context, cluster string, gvr schema.GroupVersionResource,
		namespace string, opts WatchOptions,
	) (watch.Interface, error)

	// ListEvents returns events matching the given options.
	// Used by DescribeResource to fetch events via involvedObject.uid.
	ListEvents(ctx context.Context, cluster, namespace string, opts ListOptions) (*unstructured.UnstructuredList, error)
}

// ---------------------------------------------------------------------------
// Options types
// ---------------------------------------------------------------------------

// ListOptions configures a resource list or event list query.
// Mirrors the commonly used fields of metav1.ListOptions.
type ListOptions struct {
	LabelSelector string
	FieldSelector string
	Limit         int64
	Continue      string
}

// ApplyOptions configures a server-side apply operation.
// Mirrors the commonly used fields of metav1.PatchOptions.
type ApplyOptions struct {
	Force        bool
	FieldManager string
}

// DeleteOptions configures a resource deletion.
// Mirrors the commonly used fields of metav1.DeleteOptions.
type DeleteOptions struct {
	GracePeriodSeconds *int64
}

// WatchOptions configures a watch stream.
// Mirrors the commonly used fields of metav1.ListOptions for watch.
type WatchOptions struct {
	LabelSelector     string
	FieldSelector     string
	ResourceVersion   string
	SendInitialEvents bool
}

// ---------------------------------------------------------------------------
// Cache types
// ---------------------------------------------------------------------------

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

// versionCacheEntry pairs a cached server version with its expiration.
type versionCacheEntry struct {
	version   *version.Info
	expiresAt time.Time
}

// ---------------------------------------------------------------------------
// Use case
// ---------------------------------------------------------------------------

// ResourceUseCase provides the application-level API for managing
// Kubernetes resources across multiple clusters. It validates GVRs
// via the DiscoveryClient, caches OpenAPI schemas with singleflight
// deduplication, and strips noisy metadata (managedFields,
// last-applied-configuration) from list results.
type ResourceUseCase struct {
	discovery DiscoveryClient
	resource  ResourceRepo

	mu             sync.RWMutex
	schemaCache    map[string]*schemaCacheEntry
	versionCache   map[string]*versionCacheEntry
	schemaFlights  singleflight.Group
	versionFlights singleflight.Group
}

// NewResourceUseCase returns a ResourceUseCase wired to the given
// discovery and resource backends.
func NewResourceUseCase(discovery DiscoveryClient, resource ResourceRepo) *ResourceUseCase {
	return &ResourceUseCase{
		discovery:    discovery,
		resource:     resource,
		schemaCache:  make(map[string]*schemaCacheEntry),
		versionCache: make(map[string]*versionCacheEntry),
	}
}

// ServerResources returns all API resource lists from the target cluster.
func (uc *ResourceUseCase) ServerResources(ctx context.Context, cluster string) ([]*metav1.APIResourceList, error) {
	return uc.discovery.ServerResources(ctx, cluster)
}

// ResolveSchema fetches the OpenAPI schema for the given GVK. Results
// are cached for schemaCacheTTL and concurrent requests for the same
// key are deduplicated via singleflight.
func (uc *ResourceUseCase) ResolveSchema(
	ctx context.Context,
	cluster, group, version, kind string,
) (*spec.Schema, error) {
	key := uc.schemaCacheKey(cluster, group, version, kind)

	uc.mu.RLock()
	entry, ok := uc.schemaCache[key]
	uc.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.schema, nil
	}

	v, err, _ := uc.schemaFlights.Do(key, func() (any, error) {
		// Use a non-cancellable context with its own timeout so that
		// a single caller's cancellation does not fail all waiters
		// sharing this singleflight key.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		resolved, err := uc.discovery.ResolveSchema(fetchCtx, cluster, group, version, kind)
		if err != nil {
			return nil, err
		}

		uc.mu.Lock()
		uc.evictExpired()
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
func (uc *ResourceUseCase) ListResources(
	ctx context.Context,
	cluster, group, version, resource, namespace string,
	opts ListOptions,
) (*unstructured.UnstructuredList, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	list, err := uc.resource.List(ctx, cluster, gvr, namespace, opts)
	if err != nil {
		return nil, err
	}

	for i := range list.Items {
		uc.cleanObject(&list.Items[i])
	}

	return list, nil
}

// GetResource validates the GVR and fetches a single resource.
func (uc *ResourceUseCase) GetResource(
	ctx context.Context,
	cluster, group, version, resource, namespace, name string,
) (*unstructured.Unstructured, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	return uc.resource.Get(ctx, cluster, gvr, namespace, name)
}

// DescribeResource validates the GVR, fetches the resource, extracts
// its UID, then queries related Kubernetes events filtered by
// involvedObject.uid. This is the backend equivalent of
// `kubectl describe`.
func (uc *ResourceUseCase) DescribeResource(
	ctx context.Context,
	cluster, group, version, resource, namespace, name string,
) (*unstructured.Unstructured, *unstructured.UnstructuredList, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, nil, err
	}

	obj, err := uc.resource.Get(ctx, cluster, gvr, namespace, name)
	if err != nil {
		return nil, nil, err
	}

	uid := string(obj.GetUID())

	events, err := uc.resource.ListEvents(ctx, cluster, namespace, ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.uid=%s", uid),
	})
	if err != nil {
		// Events are supplementary; return the resource even if event
		// listing fails (e.g. RBAC restrictions on events).
		return obj, &unstructured.UnstructuredList{}, nil
	}

	return obj, events, nil
}

// CreateResource validates the GVR, decodes the YAML manifest, and
// creates the resource on the target cluster.
func (uc *ResourceUseCase) CreateResource(
	ctx context.Context,
	cluster, group, version, resource, namespace string,
	manifest []byte,
) (*unstructured.Unstructured, error) {
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
func (uc *ResourceUseCase) ApplyResource(
	ctx context.Context,
	cluster, group, version, resource, namespace, name string,
	manifest []byte,
	opts ApplyOptions,
) (*unstructured.Unstructured, error) {
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

	return uc.resource.Apply(ctx, cluster, gvr, namespace, name, data, opts)
}

// DeleteResource validates the GVR and deletes the named resource.
func (uc *ResourceUseCase) DeleteResource(
	ctx context.Context,
	cluster, group, version, resource, namespace, name string,
	opts DeleteOptions,
) error {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return err
	}

	return uc.resource.Delete(ctx, cluster, gvr, namespace, name, opts)
}

// WatchResource validates the GVR and opens a long-lived watch stream.
// If the cluster supports the WatchList feature (Kubernetes >= 1.34),
// initial events are streamed before switching to change notifications.
func (uc *ResourceUseCase) WatchResource(
	ctx context.Context,
	cluster, group, version, resource, namespace string,
	opts WatchOptions,
) (watch.Interface, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return nil, err
	}

	watchListFeature, err := uc.watchListFeature(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts.SendInitialEvents = watchListFeature
	return uc.resource.Watch(ctx, cluster, gvr, namespace, opts)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

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
		return nil, &ErrInvalidInput{Field: "manifest", Message: fmt.Sprintf("invalid YAML: %s", err)}
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
// The server version is cached per cluster to avoid an extra RPC on
// every Watch call.
// See https://kubernetes.io/docs/reference/using-api/api-concepts/#streaming-lists
func (uc *ResourceUseCase) watchListFeature(ctx context.Context, cluster string) (bool, error) {
	info, err := uc.serverVersion(ctx, cluster)
	if err != nil {
		return false, err
	}

	kubeVersion, err := semver.NewVersion(info.String())
	if err != nil {
		return false, err
	}

	return kubeVersion.GreaterThanEqual(minWatchListVersion), nil
}

// serverVersion returns the cached Kubernetes version for the given
// cluster. Results are cached for schemaCacheTTL and concurrent
// requests are deduplicated via singleflight.
func (uc *ResourceUseCase) serverVersion(ctx context.Context, cluster string) (*version.Info, error) {
	uc.mu.RLock()
	entry, ok := uc.versionCache[cluster]
	uc.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.version, nil
	}

	v, err, _ := uc.versionFlights.Do(cluster, func() (any, error) {
		// Use a non-cancellable context with its own timeout so that
		// a single caller's cancellation does not fail all waiters
		// sharing this singleflight key.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		info, err := uc.discovery.ServerVersion(fetchCtx, cluster)
		if err != nil {
			return nil, err
		}

		uc.mu.Lock()
		uc.evictExpired()
		uc.versionCache[cluster] = &versionCacheEntry{
			version:   info,
			expiresAt: time.Now().Add(schemaCacheTTL),
		}
		uc.mu.Unlock()

		return info, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*version.Info), nil
}

// evictExpired removes expired entries from the schema and version
// caches. Must be called with mu held for writing.
func (uc *ResourceUseCase) evictExpired() {
	now := time.Now()
	for key, entry := range uc.schemaCache {
		if now.After(entry.expiresAt) {
			delete(uc.schemaCache, key)
		}
	}
	for key, entry := range uc.versionCache {
		if now.After(entry.expiresAt) {
			delete(uc.versionCache, key)
		}
	}
}
