package ssa

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Apply(ctx context.Context, c client.Client, obj runtime.Object, manager string) error {
	body, err := unstructuredOf(obj)
	if err != nil {
		return err
	}
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(body), client.ForceOwnership, client.FieldOwner(manager))
}

func ApplyStatus(ctx context.Context, c client.Client, obj runtime.Object, manager string) error {
	body, err := statusBody(obj)
	if err != nil {
		return err
	}
	force := true
	return c.Status().Apply(ctx, client.ApplyConfigurationFromUnstructured(body), &client.SubResourceApplyOptions{ApplyOptions: client.ApplyOptions{Force: &force, FieldManager: manager}})
}

func statusBody(obj runtime.Object) (*unstructured.Unstructured, error) {
	body, err := unstructuredOf(obj)
	if err != nil {
		return nil, err
	}
	delete(body.Object, "spec")
	return body, nil
}

func unstructuredOf(obj runtime.Object) (*unstructured.Unstructured, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.DeepCopy(), nil
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.RemoveNestedField(content, "metadata", "creationTimestamp")
	return &unstructured.Unstructured{Object: content}, nil
}
