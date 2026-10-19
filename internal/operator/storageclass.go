package operator

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	return client.IgnoreNotFound(c.Delete(ctx, live, client.Preconditions{UID: ptr.To(live.GetUID())}))
}

func immutableFieldsDiffer(live, want *unstructured.Unstructured) bool {
	for _, path := range immutableClassFields {
		liveValue, _, _ := unstructured.NestedFieldNoCopy(live.Object, path...)
		wantValue, _, _ := unstructured.NestedFieldNoCopy(want.Object, path...)
		if !reflect.DeepEqual(liveValue, wantValue) {
			return true
		}
	}
	return false
}
