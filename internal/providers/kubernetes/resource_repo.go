package kubernetes

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// resourceRepo implements core.ResourceRepo by delegating to the
// Kubernetes dynamic client, accessed through the tunnel.
type resourceRepo struct {
	kubernetes *Kubernetes
}

// NewResourceRepo returns a core.ResourceRepo backed by the Kubernetes
// dynamic API.
func NewResourceRepo(kubernetes *Kubernetes) core.ResourceRepo {
	return &resourceRepo{
		kubernetes: kubernetes,
	}
}

var _ core.ResourceRepo = (*resourceRepo)(nil)

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// List returns a paged list of resources matching the given selectors.
func (r *resourceRepo) List(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, labelSelector, fieldSelector string,
	limit int64,
	continueToken string,
) (*unstructured.UnstructuredList, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
		Limit:         limit,
		Continue:      continueToken,
	}

	return client.Resource(gvr).Namespace(namespace).List(ctx, opts)
}

// Get returns a single resource by name.
func (r *resourceRepo) Get(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, name string,
) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	return client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// Create creates a new resource from the given object.
func (r *resourceRepo) Create(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace string,
	obj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	return client.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
}

// Apply performs a server-side apply (PATCH with ApplyPatchType) for
// the given resource. When force is true, conflicts are resolved in
// favour of the caller's field manager.
func (r *resourceRepo) Apply(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, name string,
	data []byte,
	force bool,
	fieldManager string,
) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.PatchOptions{
		Force:        &force,
		FieldManager: fieldManager,
	}

	return client.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.ApplyPatchType, data, opts)
}

// Delete removes a resource. An optional gracePeriodSeconds overrides
// the default deletion grace period.
func (r *resourceRepo) Delete(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, name string,
	gracePeriodSeconds *int64,
) error {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return err
	}

	opts := metav1.DeleteOptions{
		GracePeriodSeconds: gracePeriodSeconds,
	}

	return client.Resource(gvr).Namespace(namespace).Delete(ctx, name, opts)
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

// Watch opens a long-lived watch stream for resources matching the
// given selectors. When sendInitialEvents is true, the server streams
// the current state before switching to change notifications (requires
// Kubernetes >= 1.34).
func (r *resourceRepo) Watch(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, labelSelector, fieldSelector, resourceVersion string,
	sendInitialEvents bool,
) (watch.Interface, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.ListOptions{
		LabelSelector:       labelSelector,
		FieldSelector:       fieldSelector,
		Watch:               true,
		AllowWatchBookmarks: true,
		ResourceVersion:     resourceVersion,
	}

	if sendInitialEvents {
		opts.ResourceVersionMatch = metav1.ResourceVersionMatchNotOlderThan
		opts.SendInitialEvents = &sendInitialEvents
	}

	return client.Resource(gvr).Namespace(namespace).Watch(ctx, opts)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// eventsGVR is the GroupVersionResource for core/v1 Events.
var eventsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}

// ListEvents returns events matching the given field selector. This is
// used by DescribeResource to fetch events related to a specific
// resource via involvedObject.uid.
func (r *resourceRepo) ListEvents(
	ctx context.Context,
	cluster, namespace, fieldSelector string,
) (*unstructured.UnstructuredList, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.ListOptions{
		FieldSelector: fieldSelector,
	}

	return client.Resource(eventsGVR).Namespace(namespace).List(ctx, opts)
}

// ---------------------------------------------------------------------------
// Client helpers
// ---------------------------------------------------------------------------

// client builds an impersonated dynamic client for the given cluster.
func (r *resourceRepo) client(ctx context.Context, cluster string) (*dynamic.DynamicClient, error) {
	config, err := r.kubernetes.impersonationConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(config)
}
