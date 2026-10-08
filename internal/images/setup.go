package images

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
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
	plan, err := reconcileSubject(ctx, r.Client, SubjectOfImage(img, tenant, project, false), img.Status, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	err = writeStatus(ctx, r.Client, &img, imageStatus, plan.Result, now)
	return ctrl.Result{RequeueAfter: plan.Requeue}, err
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
	plan, err := reconcileSubject(ctx, r.Client, SubjectOfClusterImage(img, false), img.Status, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	err = writeStatus(ctx, r.Client, &img, clusterImageStatus, plan.Result, now)
	return ctrl.Result{RequeueAfter: plan.Requeue}, err
}

func imageStatus(img *v1alpha1.Image) *v1alpha1.ImageStatus {
	return &img.Status
}

func clusterImageStatus(img *v1alpha1.ClusterImage) *v1alpha1.ImageStatus {
	return &img.Status
}

func SetupWithManager(mgr ctrl.Manager) error {
	if _, err := mgr.GetRESTMapper().RESTMapping(DataVolumeGVK.GroupKind(), DataVolumeGVK.Version); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			mgr.GetLogger().WithName("images").Info("images controllers skipped: CDI kinds are not installed")
			return nil
		}
		return err
	}
	if err := ownedKinds(ctrl.NewControllerManagedBy(mgr).Named("image").For(&v1alpha1.Image{})).Complete(&ImageReconciler{Client: mgr.GetClient(), Now: time.Now}); err != nil {
		return err
	}
	return ownedKinds(ctrl.NewControllerManagedBy(mgr).Named("clusterimage").For(&v1alpha1.ClusterImage{})).Complete(&ClusterImageReconciler{Client: mgr.GetClient(), Now: time.Now})
}

func ownedKinds(builder *ctrl.Builder) *ctrl.Builder {
	for _, gvk := range []schema.GroupVersionKind{DataVolumeGVK, DataImportCronGVK, DataSourceGVK, VolumeSnapshotGVK} {
		builder = builder.Owns(unstructuredOf(gvk))
	}
	return builder
}

func unstructuredOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}
