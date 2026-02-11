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

type resourceRepo struct {
	kubernetes *Kubernetes
}

func NewResourceRepo(kubernetes *Kubernetes) core.ResourceRepo {
	return &resourceRepo{
		kubernetes: kubernetes,
	}
}

var _ core.ResourceRepo = (*resourceRepo)(nil)

func (r *resourceRepo) List(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error) {
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

func (r *resourceRepo) Get(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.GetOptions{}

	return client.Resource(gvr).Namespace(namespace).Get(ctx, name, opts)
}

func (r *resourceRepo) Create(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.CreateOptions{}

	return client.Resource(gvr).Namespace(namespace).Create(ctx, obj, opts)
}

func (r *resourceRepo) Apply(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, data []byte, force bool, fieldManager string) (*unstructured.Unstructured, error) {
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

func (r *resourceRepo) Delete(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, gracePeriodSeconds *int64) error {
	client, err := r.client(ctx, cluster)
	if err != nil {
		return err
	}

	opts := metav1.DeleteOptions{
		GracePeriodSeconds: gracePeriodSeconds,
	}

	return client.Resource(gvr).Namespace(namespace).Delete(ctx, name, opts)
}

func (r *resourceRepo) Watch(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string, sendInitialEvents bool) (watch.Interface, error) {
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

func (r *resourceRepo) client(ctx context.Context, cluster string) (*dynamic.DynamicClient, error) {
	config, err := r.kubernetes.impersonationConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(config)
}
