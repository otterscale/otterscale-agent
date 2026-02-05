package kubernetes

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

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

func (r *resourceRepo) List(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace, labelSelector, fieldSelector string, limit int64, continueToken string) (*unstructured.UnstructuredList, error) {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
		Limit:         limit,
		Continue:      continueToken,
	}

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).List(ctx, opts)
}

func (r *resourceRepo) Get(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.GetOptions{}

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).Get(ctx, name, opts)
}

func (r *resourceRepo) Create(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.CreateOptions{}

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).Create(ctx, obj, opts)
}

func (r *resourceRepo) Apply(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace, name string, data []byte, force bool, fieldManager string) (*unstructured.Unstructured, error) {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
	if err != nil {
		return nil, err
	}

	opts := metav1.PatchOptions{
		Force:        &force,
		FieldManager: fieldManager,
	}

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).Patch(ctx, name, types.ApplyPatchType, data, opts)
}

func (r *resourceRepo) Delete(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace, name string, gracePeriodSeconds *int64) error {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
	if err != nil {
		return err
	}

	opts := metav1.DeleteOptions{
		GracePeriodSeconds: gracePeriodSeconds,
	}

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).Delete(ctx, name, opts)
}

func (r *resourceRepo) Watch(ctx context.Context, cgvr core.ClusterGroupVersionResource, namespace, labelSelector, fieldSelector, resourceVersion string, sendInitialEvents bool) (watch.Interface, error) {
	client, err := r.kubernetes.dynamic(ctx, cgvr.Cluster)
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

	return client.Resource(cgvr.GroupVersionResource).Namespace(namespace).Watch(ctx, opts)
}
