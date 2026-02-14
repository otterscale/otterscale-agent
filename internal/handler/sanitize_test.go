package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCleanObject_RemovesManagedFields(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":          "test-pod",
				"managedFields": []any{"field1", "field2"},
			},
		},
	}

	cleanObject(obj)

	metadata := obj.Object["metadata"].(map[string]any)
	if _, exists := metadata["managedFields"]; exists {
		t.Error("managedFields should have been removed")
	}
}

func TestCleanObject_RemovesLastAppliedAnnotation(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "test-pod",
				"annotations": map[string]any{
					"kubectl.kubernetes.io/last-applied-configuration": `{"some":"config"}`,
					"other-annotation": "keep-this",
				},
			},
		},
	}

	cleanObject(obj)

	annotations := obj.GetAnnotations()
	if _, exists := annotations["kubectl.kubernetes.io/last-applied-configuration"]; exists {
		t.Error("last-applied-configuration annotation should have been removed")
	}
	if annotations["other-annotation"] != "keep-this" {
		t.Error("other annotations should be preserved")
	}
}

func TestCleanObject_RemovesAnnotationsFieldWhenEmpty(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "test-pod",
				"annotations": map[string]any{
					"kubectl.kubernetes.io/last-applied-configuration": `{"some":"config"}`,
				},
			},
		},
	}

	cleanObject(obj)

	metadata := obj.Object["metadata"].(map[string]any)
	if _, exists := metadata["annotations"]; exists {
		t.Error("annotations field should have been removed when empty")
	}
}

func TestCleanObject_NoOpWhenClean(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "test-pod",
			},
		},
	}

	// Should not panic or modify anything.
	cleanObject(obj)

	if obj.GetName() != "test-pod" {
		t.Error("name should be unchanged")
	}
}
