package operator

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	k0sStackLabel           = "k0s.k0sproject.io/stack"
	k0sCoreDNSStack         = "coredns"
	k0sChecksumAnnotation   = "k0s.k0sproject.io/stack-checksum"
	k0sLastConfigAnnotation = "k0s.k0sproject.io/last-applied-configuration"
)

var serviceKeepsFromLive = []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"}

func adoptK0sCoreDNS(c client.Client) func(context.Context, release.Group) error {
	return func(ctx context.Context, group release.Group) error {
		if group.Name != dnsGroup {
			return nil
		}
		for _, desired := range group.Objects {
			if err := replaceK0sObject(ctx, c, desired); err != nil {
				return err
			}
		}
		return markTakeoverDone(ctx, c)
	}
}

func markTakeoverDone(ctx context.Context, c client.Client) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"done"}}}`, v1alpha1.AnnotationK0sCoreDNSTakeover)
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}}
	return c.Patch(ctx, cluster, client.RawPatch(types.MergePatchType, []byte(patch)))
}

func replaceK0sObject(ctx context.Context, c client.Client, desired *unstructured.Unstructured) error {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(desired.GroupVersionKind())
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), live)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if live.GetLabels()[k0sStackLabel] != k0sCoreDNSStack {
		return nil
	}
	return c.Update(ctx, replacementOf(desired, live), client.FieldOwner(release.FieldManager))
}

func replacementOf(desired, live *unstructured.Unstructured) *unstructured.Unstructured {
	replacement := desired.DeepCopy()
	replacement.SetResourceVersion(live.GetResourceVersion())
	annotations := replacement.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for _, key := range []string{k0sChecksumAnnotation, k0sLastConfigAnnotation} {
		if value, ok := live.GetAnnotations()[key]; ok {
			annotations[key] = value
		}
	}
	replacement.SetAnnotations(annotations)
	switch live.GetKind() {
	case "Service":
		keepFields(replacement, live, serviceKeepsFromLive)
	case "Deployment":
		keepFields(replacement, live, []string{"replicas"})
	}
	return replacement
}

func keepFields(replacement, live *unstructured.Unstructured, fields []string) {
	for _, field := range fields {
		if value, found, _ := unstructured.NestedFieldCopy(live.Object, "spec", field); found {
			_ = unstructured.SetNestedField(replacement.Object, value, "spec", field)
		}
	}
}

func beforeGroupHooks(c client.Client) func(context.Context, release.Group) error {
	authn := authnBootstrapHook(c)
	dns := adoptK0sCoreDNS(c)
	return func(ctx context.Context, group release.Group) error {
		if err := authn(ctx, group); err != nil {
			return err
		}
		return dns(ctx, group)
	}
}
