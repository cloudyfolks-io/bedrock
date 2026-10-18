package network

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var attachmentGVK = schema.GroupVersionKind{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinition"}

func newObject(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	return u
}

func getObject(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	u := newObject(gvk, name)
	if err := c.Get(ctx, client.ObjectKey{Name: name}, u); err != nil {
		return nil, err
	}
	return u, nil
}

func listObjects(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, opts ...client.ListOption) ([]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := c.List(ctx, list, opts...); err != nil {
		return nil, err
	}
	items := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		items[i] = &list.Items[i]
		items[i].SetGroupVersionKind(gvk)
	}
	return items, nil
}

func deleteObject(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, name string) error {
	return client.IgnoreNotFound(c.Delete(ctx, newObject(gvk, name)))
}

func exists(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, name string) (bool, error) {
	_, err := getObject(ctx, c, gvk, name)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func ownedBy(u *unstructured.Unstructured, owner metav1.Object) bool {
	_, uid, ok := OwnerOf(u)
	return ok && uid == owner.GetUID()
}

func liveOwnedObject(ctx context.Context, c client.Reader, gvk schema.GroupVersionKind, name string, owner metav1.Object) (bool, error) {
	existing, err := getObject(ctx, c, gvk, name)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ownedBy(existing, owner) && existing.GetDeletionTimestamp().IsZero(), nil
}
