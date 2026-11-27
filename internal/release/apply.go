package release

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type Applier struct {
	Client client.Client
}

func (a Applier) Apply(ctx context.Context, group Group) error {
	for _, obj := range group.Objects {
		if err := a.applyObject(ctx, obj); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

func (a Applier) applyObject(ctx context.Context, obj *unstructured.Unstructured) error {
	err := ssa.Apply(ctx, a.Client, obj, FieldManager)
	if !immutableJob(obj, err) {
		return err
	}
	if err := a.Client.Delete(ctx, obj, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return ssa.Apply(ctx, a.Client, obj, FieldManager)
}

func immutableJob(obj *unstructured.Unstructured, err error) bool {
	return err != nil && obj.GroupVersionKind().GroupKind() == (schema.GroupKind{Group: "batch", Kind: "Job"}) && errors.IsInvalid(err) && strings.Contains(err.Error(), "immutable")
}

func (a Applier) Prune(ctx context.Context, previous, current Inventory) error {
	for _, ref := range previous.sorted() {
		if _, keep := current[ref]; keep || neverPrune(ref) {
			continue
		}
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(ref.APIVersion)
		obj.SetKind(ref.Kind)
		obj.SetNamespace(ref.Namespace)
		obj.SetName(ref.Name)
		if err := a.Client.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return fmt.Errorf("prune %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
		}
	}
	return nil
}

func neverPrune(ref ObjectRef) bool {
	return ref.Kind == "Namespace" || ref.Kind == "CustomResourceDefinition"
}
