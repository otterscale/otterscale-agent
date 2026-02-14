package kubernetes

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
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

// List returns a paged list of resources matching the given options.
func (r *resourceRepo) List(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace string,
	opts core.ListOptions,
) (*unstructured.UnstructuredList, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	listOpts := metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
		FieldSelector: opts.FieldSelector,
		Limit:         opts.Limit,
		Continue:      opts.Continue,
	}

	result, err := client.Resource(gvr).Namespace(namespace).List(ctx, listOpts)
	return result, wrapK8sError(err)
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

	result, err := client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	return result, wrapK8sError(err)
}

// Create decodes a YAML manifest and creates the resource.
func (r *resourceRepo) Create(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace string,
	manifest []byte,
) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	obj, err := fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	result, err := client.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return result, wrapK8sError(err)
}

// Apply decodes a YAML manifest, converts it to JSON, and performs a
// server-side apply (PATCH with ApplyPatchType). When force is true,
// conflicts are resolved in favour of the caller's field manager.
func (r *resourceRepo) Apply(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, name string,
	manifest []byte,
	opts core.ApplyOptions,
) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	obj, err := fromYAML(manifest)
	if err != nil {
		return nil, err
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, &core.DomainError{Code: core.ErrorCodeInternal, Message: "marshal manifest to JSON", Cause: err}
	}

	patchOpts := metav1.PatchOptions{
		Force:        &opts.Force,
		FieldManager: opts.FieldManager,
	}

	result, err := client.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.ApplyPatchType, data, patchOpts)
	return result, wrapK8sError(err)
}

// Delete removes a resource.
func (r *resourceRepo) Delete(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace, name string,
	opts core.DeleteOptions,
) error {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return err
	}

	deleteOpts := metav1.DeleteOptions{
		GracePeriodSeconds: opts.GracePeriodSeconds,
	}

	return wrapK8sError(client.Resource(gvr).Namespace(namespace).Delete(ctx, name, deleteOpts))
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

// Watch opens a long-lived watch stream for resources matching the
// given options. When SendInitialEvents is true, the server streams
// the current state before switching to change notifications (requires
// Kubernetes >= 1.34).
func (r *resourceRepo) Watch(
	ctx context.Context,
	cluster string,
	gvr schema.GroupVersionResource,
	namespace string,
	opts core.WatchOptions,
) (watch.Interface, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	listOpts := metav1.ListOptions{
		LabelSelector:       opts.LabelSelector,
		FieldSelector:       opts.FieldSelector,
		Watch:               true,
		AllowWatchBookmarks: true,
		ResourceVersion:     opts.ResourceVersion,
	}

	if opts.SendInitialEvents {
		listOpts.ResourceVersionMatch = metav1.ResourceVersionMatchNotOlderThan
		listOpts.SendInitialEvents = &opts.SendInitialEvents
	}

	result, err := client.Resource(gvr).Namespace(namespace).Watch(ctx, listOpts)
	return result, wrapK8sError(err)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// eventsGVR is the GroupVersionResource for core/v1 Events.
var eventsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}

// ListEvents returns events matching the given options. This is
// used by DescribeResource to fetch events related to a specific
// resource via involvedObject.uid.
func (r *resourceRepo) ListEvents(
	ctx context.Context,
	cluster, namespace string,
	opts core.ListOptions,
) (*unstructured.UnstructuredList, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	listOpts := metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
		FieldSelector: opts.FieldSelector,
		Limit:         opts.Limit,
		Continue:      opts.Continue,
	}

	result, err := client.Resource(eventsGVR).Namespace(namespace).List(ctx, listOpts)
	return result, wrapK8sError(err)
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

// fromYAML decodes a YAML manifest into an Unstructured object.
// Returns a domain validation error if the manifest is invalid.
func fromYAML(manifest []byte) (*unstructured.Unstructured, error) {
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj := &unstructured.Unstructured{}

	if _, _, err := dec.Decode(manifest, nil, obj); err != nil {
		return nil, &core.ErrInvalidInput{Field: "manifest", Message: fmt.Sprintf("invalid YAML: %s", err)}
	}

	return obj, nil
}
