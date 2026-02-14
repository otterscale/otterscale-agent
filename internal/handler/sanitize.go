package handler

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// cleanObject strips noisy metadata that clutters list output:
//   - metadata.managedFields (server-side apply bookkeeping)
//   - the kubectl.kubernetes.io/last-applied-configuration annotation
//
// This is a presentation concern: the domain layer returns raw
// Kubernetes objects and the handler sanitises them before serialising
// to protobuf.
func cleanObject(obj *unstructured.Unstructured) {
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
