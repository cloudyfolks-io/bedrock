package platform

import (
	"context"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	AnnotationOwnerUID       = "bedrock.cloudyfolks.io/owner-uid"
	AnnotationOwnerNamespace = "bedrock.cloudyfolks.io/owner-namespace"
	finalizerPrefix          = "bedrock.cloudyfolks.io/"
)

type Owner struct {
	Kind      string
	Name      string
	Namespace string
	UID       types.UID
	Tenant    string
	Project   string
}

func Labels(kind, name, tenant, project string) map[string]string {
	return v1alpha1.TenantLabels(kind, name, tenant, project)
}

func Finalizer(kind string) string {
	var name strings.Builder
	for i, r := range kind {
		if unicode.IsUpper(r) && i > 0 {
			name.WriteByte('-')
		}
		name.WriteRune(unicode.ToLower(r))
	}
	return finalizerPrefix + name.String()
}

func Adopt(obj *unstructured.Unstructured, owner Owner) *unstructured.Unstructured {
	adopted := obj.DeepCopy()
	labels := adopted.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for key, value := range Labels(owner.Kind, owner.Name, owner.Tenant, owner.Project) {
		labels[key] = value
	}
	adopted.SetLabels(labels)
	annotations := adopted.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationOwnerUID] = string(owner.UID)
	if owner.Namespace != "" {
		annotations[AnnotationOwnerNamespace] = owner.Namespace
	}
	adopted.SetAnnotations(annotations)
	return adopted
}

func OwnerOf(obj *unstructured.Unstructured) Owner {
	labels, annotations := obj.GetLabels(), obj.GetAnnotations()
	return Owner{
		Kind:      labels[v1alpha1.LabelKind],
		Name:      labels[v1alpha1.LabelName],
		Namespace: annotations[AnnotationOwnerNamespace],
		UID:       types.UID(annotations[AnnotationOwnerUID]),
		Tenant:    labels[v1alpha1.LabelTenant],
		Project:   labels[v1alpha1.LabelProject],
	}
}

func SweepOrphans(ctx context.Context, c client.Client, gvks []schema.GroupVersionKind, ownerExists func(context.Context, Owner) (bool, error), dryRun bool) ([]*unstructured.Unstructured, error) {
	orphans := []*unstructured.Unstructured{}
	for _, gvk := range gvks {
		found, err := orphansOf(ctx, c, gvk, ownerExists)
		if err != nil {
			return orphans, err
		}
		orphans = append(orphans, found...)
	}
	if dryRun {
		return orphans, nil
	}
	for _, orphan := range orphans {
		if err := c.Delete(ctx, orphan); err != nil && !apierrors.IsNotFound(err) {
			return orphans, err
		}
	}
	return orphans, nil
}

func orphansOf(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, ownerExists func(context.Context, Owner) (bool, error)) ([]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	err := c.List(ctx, list, client.HasLabels{v1alpha1.LabelKind})
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	orphans := []*unstructured.Unstructured{}
	for i := range list.Items {
		item := &list.Items[i]
		if !item.GetDeletionTimestamp().IsZero() {
			continue
		}
		exists, err := ownerExists(ctx, OwnerOf(item))
		if err != nil {
			return nil, err
		}
		if !exists {
			orphans = append(orphans, item)
		}
	}
	return orphans, nil
}
