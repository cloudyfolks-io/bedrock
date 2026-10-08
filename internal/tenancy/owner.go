package tenancy

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func WithOwner[T client.Object](obj T, owner metav1.Object, kind schema.GroupVersionKind) T {
	owned := obj.DeepCopyObject().(T)
	owned.SetOwnerReferences(append(owned.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: kind.GroupVersion().String(),
		Kind:       kind.Kind,
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
		Controller: ptr.To(true),
	}))
	return owned
}
