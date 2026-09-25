package release

import (
	"context"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

const (
	SystemNamespace        = "bedrock-system"
	InventoryConfigMapName = "bedrock-release-inventory"
	inventoryKey           = "objects"
)

type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

type Inventory map[ObjectRef]struct{}

func RefOf(obj *unstructured.Unstructured) ObjectRef {
	return ObjectRef{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
}

func InventoryOf(groups []Group) Inventory {
	inv := Inventory{}
	for _, group := range groups {
		for _, obj := range group.Objects {
			inv[RefOf(obj)] = struct{}{}
		}
	}
	return inv
}

func (inv Inventory) sorted() []ObjectRef {
	refs := make([]ObjectRef, 0, len(inv))
	for ref := range inv {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.APIVersion != b.APIVersion {
			return a.APIVersion < b.APIVersion
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return refs
}

func ReadInventory(ctx context.Context, c client.Client) (Inventory, error) {
	var cm corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: SystemNamespace, Name: InventoryConfigMapName}, &cm)
	if errors.IsNotFound(err) {
		return Inventory{}, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []ObjectRef
	if err := json.Unmarshal([]byte(cm.Data[inventoryKey]), &refs); err != nil {
		return nil, err
	}
	inv := Inventory{}
	for _, ref := range refs {
		inv[ref] = struct{}{}
	}
	return inv, nil
}

func WriteInventory(ctx context.Context, c client.Client, inv Inventory) error {
	raw, err := json.Marshal(inv.sorted())
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Namespace: SystemNamespace, Name: InventoryConfigMapName, Labels: map[string]string{v1alpha1.LabelKind: "Inventory", v1alpha1.LabelName: InventoryConfigMapName}},
		Data:       map[string]string{inventoryKey: string(raw)},
	}
	return ssa.Apply(ctx, c, cm, FieldManager)
}
