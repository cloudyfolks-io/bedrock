package addons

import (
	"context"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const inventoryKey = "objects"

func inventoryName(addon string) string {
	return "bedrock-addon-" + addon + "-inventory"
}

func inventoryOf(objects []*unstructured.Unstructured) release.Inventory {
	inventory := release.Inventory{}
	for _, obj := range objects {
		inventory[release.RefOf(obj)] = struct{}{}
	}
	return inventory
}

func sortedRefs(inventory release.Inventory) []release.ObjectRef {
	refs := make([]release.ObjectRef, 0, len(inventory))
	for ref := range inventory {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		switch {
		case a.APIVersion != b.APIVersion:
			return a.APIVersion < b.APIVersion
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.Namespace != b.Namespace:
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return refs
}

func readInventory(ctx context.Context, c client.Client, addon string) (release.Inventory, error) {
	var config corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: inventoryName(addon)}, &config)
	if apierrors.IsNotFound(err) {
		return release.Inventory{}, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []release.ObjectRef
	if err := json.Unmarshal([]byte(config.Data[inventoryKey]), &refs); err != nil {
		return nil, err
	}
	inventory := release.Inventory{}
	for _, ref := range refs {
		inventory[ref] = struct{}{}
	}
	return inventory, nil
}

func writeInventory(ctx context.Context, c client.Client, addon string, inventory release.Inventory) error {
	raw, err := json.Marshal(sortedRefs(inventory))
	if err != nil {
		return err
	}
	config := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: release.SystemNamespace,
			Name:      inventoryName(addon),
			Labels:    map[string]string{v1alpha1.LabelKind: "Addon", v1alpha1.LabelName: addon},
		},
		Data: map[string]string{inventoryKey: string(raw)},
	}
	return ssa.Apply(ctx, c, config, v1alpha1.AddonsFieldManager)
}

func deleteInventory(ctx context.Context, c client.Client, addon string) error {
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: inventoryName(addon)}}
	return client.IgnoreNotFound(c.Delete(ctx, config))
}
