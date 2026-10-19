package objectstorage

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

type NamespaceReconciler struct {
	Client client.Client
}

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := namespaceMeta()
	err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, ns)
	if err == nil && ns.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	remaining, err := r.hasTenantObjects(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if remaining {
		return ctrl.Result{RequeueAfter: pendingRequeue}, nil
	}
	return ctrl.Result{}, ReleaseNamespace(ctx, r.Client, req.Name)
}

func (r *NamespaceReconciler) hasTenantObjects(ctx context.Context, namespace string) (bool, error) {
	var users v1alpha1.ObjectStorageUserList
	if err := r.Client.List(ctx, &users, client.InNamespace(namespace), client.Limit(1)); err != nil {
		return false, err
	}
	var buckets v1alpha1.ObjectStorageBucketList
	if err := r.Client.List(ctx, &buckets, client.InNamespace(namespace), client.Limit(1)); err != nil {
		return false, err
	}
	return len(users.Items)+len(buckets.Items) > 0, nil
}

func ReleaseNamespace(ctx context.Context, c client.Client, namespace string) error {
	users := &unstructured.UnstructuredList{}
	users.SetGroupVersionKind(cephObjectStoreUserGVK.GroupVersion().WithKind("CephObjectStoreUserList"))
	err := c.List(ctx, users, client.InNamespace(StoreNamespace), client.HasLabels{v1alpha1.LabelKind})
	if err != nil && !meta.IsNoMatchError(err) {
		return err
	}
	for i := range users.Items {
		if platform.OwnerOf(&users.Items[i]).Namespace == namespace {
			if err := client.IgnoreNotFound(c.Delete(ctx, &users.Items[i])); err != nil {
				return err
			}
		}
	}
	account, found, err := getRook(ctx, c, cephObjectStoreAccountGVK, client.ObjectKey{Namespace: StoreNamespace, Name: AccountName(namespace)})
	if err != nil || !found || platform.OwnerOf(account).Kind != "Namespace" {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, account))
}

func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	leaving := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(e event.UpdateEvent) bool { return !e.ObjectNew.GetDeletionTimestamp().IsZero() },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-namespace").
		For(&corev1.Namespace{}, builder.OnlyMetadata, builder.WithPredicates(leaving)).
		Complete(r)
}
