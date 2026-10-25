package operator

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
		return nil
	}
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
	if live.GetKind() == "Service" {
		keepServiceAddress(replacement, live)
	}
	return replacement
}

func keepServiceAddress(replacement, live *unstructured.Unstructured) {
	for _, field := range serviceKeepsFromLive {
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
