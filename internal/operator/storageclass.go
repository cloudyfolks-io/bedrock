package operator

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var immutableClassFields = [][]string{{"provisioner"}, {"parameters"}, {"reclaimPolicy"}, {"volumeBindingMode"}}

func dropStaleStorageClass(ctx context.Context, c client.Client, want *unstructured.Unstructured) error {
	if want.GetKind() != "StorageClass" {
		return nil
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(want.GroupVersionKind())
	err := c.Get(ctx, client.ObjectKeyFromObject(want), live)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !immutableFieldsDiffer(live, want) {
		return nil
	}
	if !ownedByOperator(live) {
		return fmt.Errorf("StorageClass %s has other parameters and is not owned by %s: delete it by hand", live.GetName(), v1alpha1.OperatorFieldManager)
	}
	return client.IgnoreNotFound(c.Delete(ctx, live, client.Preconditions{UID: ptr.To(live.GetUID())}))
}

func immutableFieldsDiffer(live, want *unstructured.Unstructured) bool {
	for _, path := range immutableClassFields {
		liveValue, _, _ := unstructured.NestedFieldNoCopy(live.Object, path...)
		wantValue, found, _ := unstructured.NestedFieldNoCopy(want.Object, path...)
		if found && !reflect.DeepEqual(liveValue, wantValue) {
			return true
		}
	}
	return false
}

func ownedByOperator(obj *unstructured.Unstructured) bool {
	return slices.ContainsFunc(obj.GetManagedFields(), func(entry metav1.ManagedFieldsEntry) bool {
		return entry.Manager == v1alpha1.OperatorFieldManager
	})
}
