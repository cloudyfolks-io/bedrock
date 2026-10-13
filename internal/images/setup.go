package images

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/kinds"
)

type ImageReconciler struct {
	Client client.Client
	Now    func() time.Time
}

type ClusterImageReconciler struct {
	Client client.Client
	Now    func() time.Time
}

func (r *ImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var img v1alpha1.Image
	if err := r.Client.Get(ctx, req.NamespacedName, &img); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !img.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: img.Namespace}, &namespace); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	tenant, project := tenantOf(namespace)
	now := r.Now()
	plan, err := reconcileSubject(ctx, r.Client, SubjectOfImage(img, tenant, project), img.Status, now)
	if plan.Result.Phase == "" {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: plan.Requeue}, errors.Join(err, writeStatus(ctx, r.Client, &img, imageStatus, plan.Result, now))
}

func (r *ClusterImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var img v1alpha1.ClusterImage
	if err := r.Client.Get(ctx, req.NamespacedName, &img); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !img.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	now := r.Now()
	plan, err := reconcileSubject(ctx, r.Client, SubjectOfClusterImage(img), img.Status, now)
	if plan.Result.Phase == "" {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: plan.Requeue}, errors.Join(err, writeStatus(ctx, r.Client, &img, clusterImageStatus, plan.Result, now))
}

func imageStatus(img *v1alpha1.Image) *v1alpha1.ImageStatus {
	return &img.Status
}

func clusterImageStatus(img *v1alpha1.ClusterImage) *v1alpha1.ImageStatus {
	return &img.Status
}

func SetupWithManager(mgr ctrl.Manager) error {
	return kinds.SetupWhenServed(mgr, "images", ownedGVKs, setupControllers)
}

func setupControllers(mgr ctrl.Manager) error {
	images := ownedKinds(ctrl.NewControllerManagedBy(mgr).Named("image").For(&v1alpha1.Image{})).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(enqueueImages(mgr.GetClient())), builder.WithPredicates(platformSettings()))
	if err := images.Complete(&ImageReconciler{Client: mgr.GetClient(), Now: time.Now}); err != nil {
		return err
	}
	clusterImages := ownedKinds(ctrl.NewControllerManagedBy(mgr).Named("clusterimage").For(&v1alpha1.ClusterImage{})).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(enqueueClusterImages(mgr.GetClient())), builder.WithPredicates(platformSettings()))
	return clusterImages.Complete(&ClusterImageReconciler{Client: mgr.GetClient(), Now: time.Now})
}

func platformSettings() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == settingAdditionalCA || obj.GetName() == settingPlatformHost
	})
}

func enqueueImages(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list v1alpha1.ImageList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(list.Items))
		for _, img := range list.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&img)})
		}
		return requests
	}
}

func enqueueClusterImages(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list v1alpha1.ClusterImageList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(list.Items))
		for _, img := range list.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&img)})
		}
		return requests
	}
}

var ownedGVKs = []schema.GroupVersionKind{DataVolumeGVK, DataImportCronGVK, DataSourceGVK, VolumeSnapshotGVK}

func ownedKinds(builder *ctrl.Builder) *ctrl.Builder {
	for _, gvk := range ownedGVKs {
		builder = builder.Owns(unstructuredOf(gvk))
	}
	return builder
}

func unstructuredOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}
