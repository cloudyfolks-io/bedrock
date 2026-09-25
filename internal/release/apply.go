package release

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

type Applier struct {
	Client client.Client
}

func (a Applier) Apply(ctx context.Context, group Group) error {
	for _, obj := range group.Objects {
		if err := ssa.Apply(ctx, a.Client, obj, FieldManager); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
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
