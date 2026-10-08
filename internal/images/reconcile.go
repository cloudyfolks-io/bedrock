package images

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func reconcileSubject(ctx context.Context, c client.Client, s Subject, prev v1alpha1.ImageStatus, now time.Time) (Plan, error) {
	if s.Kind == KindClusterImage {
		present, err := catalogNamespaceExists(ctx, c)
		if err != nil {
			return Plan{}, err
		}
		if !present {
			return pending(v1alpha1.ImagePhasePending, ReasonCatalogNamespaceMissing, "namespace "+CatalogNamespace+" does not exist", 30*time.Second), nil
		}
	}
	host, err := PlatformHost(ctx, c)
	if err != nil {
		return Plan{}, err
	}
	world, err := Observe(ctx, c, s, prev, host)
	if err != nil {
		return Plan{}, err
	}
	plan := Decide(s, world, now)
	return plan, Execute(ctx, c, plan)
}

func catalogNamespaceExists(ctx context.Context, c client.Reader) (bool, error) {
	var namespace corev1.Namespace
	err := c.Get(ctx, client.ObjectKey{Name: CatalogNamespace}, &namespace)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func tenantOf(ns corev1.Namespace) (tenant, project string) {
	return ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
}

func writeStatus[T client.Object](ctx context.Context, c client.Client, obj T, statusOf func(T) *v1alpha1.ImageStatus, result Result, now time.Time) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		status := statusOf(obj)
		next := BuildStatus(*status, obj.GetGeneration(), result, metav1.NewTime(now))
		if equality.Semantic.DeepEqual(*status, next) {
			return nil
		}
		*status = next
		return c.Status().Update(ctx, obj, client.FieldOwner(v1alpha1.ImagesFieldManager))
	})
}
