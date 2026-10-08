package objectstorage

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	reasonReady             = "Ready"
	reasonBound             = "Bound"
	reasonStoreNotInstalled = "StoreNotInstalled"
	reasonStoreNotReady     = "StoreNotReady"
	reasonNamespaceNotProj  = "NamespaceNotProject"
	reasonQuotaInvalid      = "QuotaInvalid"

	pendingRequeue = 10 * time.Second
	storeRequeue   = 15 * time.Second
	steadyRequeue  = 5 * time.Minute
)

var (
	cephObjectStoreGVK     = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephObjectStore"}
	cephObjectStoreUserGVK = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephObjectStoreUser"}
	objectBucketClaimGVK   = schema.GroupVersionKind{Group: "objectbucket.io", Version: "v1alpha1", Kind: "ObjectBucketClaim"}
)

type outcome struct {
	reason  string
	message string
	requeue time.Duration
}

func (o outcome) ready() bool {
	return o.reason == reasonReady || o.reason == reasonBound
}

func condition(conditionType string, ok bool, reason, message string, generation int64) metav1.Condition {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{Type: conditionType, Status: status, Reason: reason, Message: message, ObservedGeneration: generation}
}

func updateStatus[T client.Object](ctx context.Context, c client.Client, obj T, mutate func(T)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, _ := obj.DeepCopyObject().(T)
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), latest); err != nil {
			return err
		}
		mutate(latest)
		return c.Status().Update(ctx, latest)
	})
}

func projectOf(ctx context.Context, c client.Client, namespace string) (tenant, project string, found bool, err error) {
	var ns metav1.PartialObjectMetadata
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return "", "", false, client.IgnoreNotFound(err)
	}
	labels := ns.GetLabels()
	tenant, project = labels[v1alpha1.LabelTenant], labels[v1alpha1.LabelProject]
	return tenant, project, tenant != "" && project != "", nil
}

func storeProblem(ctx context.Context, c client.Client) (outcome, error) {
	store := &unstructured.Unstructured{}
	store.SetGroupVersionKind(cephObjectStoreGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: StoreName}, store)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return outcome{reason: reasonStoreNotInstalled, message: "the object store " + StoreName + " does not exist", requeue: storeRequeue}, nil
	}
	if err != nil {
		return outcome{}, err
	}
	if phase := rookPhase(store); phase != "Ready" {
		return outcome{reason: reasonStoreNotReady, message: "the object store " + StoreName + " is in phase " + phase, requeue: storeRequeue}, nil
	}
	return outcome{}, nil
}

func getRook(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, key client.ObjectKey) (*unstructured.Unstructured, bool, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := c.Get(ctx, key, obj)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return obj, true, nil
}

func rookPhase(obj *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	return phase
}
