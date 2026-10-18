package network

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func HoldsAddress(eip v1alpha1.ElasticIP) bool {
	return eip.Status.Address != "" || eip.Status.ProviderNetwork != ""
}

func QuotaAllows(limit *int32, all []v1alpha1.ElasticIP, eip v1alpha1.ElasticIP) bool {
	if limit == nil || HoldsAddress(eip) {
		return true
	}
	ahead := int32(0)
	for _, other := range all {
		if other.UID != eip.UID && (HoldsAddress(other) || olderThan(other, eip)) {
			ahead++
		}
	}
	return ahead < *limit
}

func tenantElasticIPs(ctx context.Context, c client.Reader, tenant string) ([]v1alpha1.ElasticIP, error) {
	var namespaces corev1.NamespaceList
	if err := c.List(ctx, &namespaces, client.MatchingLabels{v1alpha1.LabelTenant: tenant}); err != nil {
		return nil, err
	}
	var all []v1alpha1.ElasticIP
	for _, namespace := range namespaces.Items {
		var list v1alpha1.ElasticIPList
		if err := c.List(ctx, &list, client.InNamespace(namespace.Name)); err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
	}
	return all, nil
}

func QuotaWaiters(ctx context.Context, c client.Reader, tenant string) []reconcile.Request {
	all, err := tenantElasticIPs(ctx, c, tenant)
	if err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, eip := range all {
		if !HoldsAddress(eip) {
			requests = append(requests, requestOf(eip.Namespace, eip.Name))
		}
	}
	return requests
}

func quotaViolation(ctx context.Context, c client.Reader, eip v1alpha1.ElasticIP, tenant string) (*Violation, error) {
	if HoldsAddress(eip) {
		return nil, nil
	}
	var owner v1alpha1.Tenant
	if err := c.Get(ctx, client.ObjectKey{Name: tenant}, &owner); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	limit := owner.Spec.Quota.ElasticIPs
	if limit == nil {
		return nil, nil
	}
	if provisioned, err := liveOwnedObject(ctx, c, fabric.OvnEipGVK, FabricName(eip.Namespace, eip.Name), &eip); err != nil || provisioned {
		return nil, err
	}
	all, err := tenantElasticIPs(ctx, c, tenant)
	if err != nil || QuotaAllows(limit, all, eip) {
		return nil, err
	}
	return &Violation{Reason: ReasonQuotaExceeded, Message: fmt.Sprintf("the tenant %s allows %d elastic IPs and older ones use them", tenant, *limit)}, nil
}
