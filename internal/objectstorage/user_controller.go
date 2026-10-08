package objectstorage

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type UserReconciler struct {
	Client client.Client
}

func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var user v1alpha1.ObjectStorageUser
	if err := r.Client.Get(ctx, req.NamespacedName, &user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !user.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &user)
	}
	if err := r.ensureFinalizer(ctx, &user); err != nil {
		return ctrl.Result{}, err
	}
	result, secret, err := r.converge(ctx, &user)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.report(ctx, &user, result, secret), nil
}

func (r *UserReconciler) ensureFinalizer(ctx context.Context, user *v1alpha1.ObjectStorageUser) error {
	if controllerutil.ContainsFinalizer(user, UserFinalizer) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageUser{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(user), latest); err != nil {
			return err
		}
		controllerutil.AddFinalizer(latest, UserFinalizer)
		if err := r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)); err != nil {
			return err
		}
		*user = *latest
		return nil
	})
}

func (r *UserReconciler) finalize(ctx context.Context, user *v1alpha1.ObjectStorageUser) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(user, UserFinalizer) {
		return ctrl.Result{}, nil
	}
	inUse, err := r.hasBuckets(ctx, user)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inUse {
		blocked := outcome{reason: "InUse", message: "buckets still name this user in spec.userRef", requeue: pendingRequeue}
		return r.report(ctx, user, blocked, ""), nil
	}
	if err := r.deleteCephUser(ctx, user); err != nil {
		return ctrl.Result{}, err
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageUser{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(user), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		controllerutil.RemoveFinalizer(latest, UserFinalizer)
		return client.IgnoreNotFound(r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)))
	})
	return ctrl.Result{}, err
}

func (r *UserReconciler) hasBuckets(ctx context.Context, user *v1alpha1.ObjectStorageUser) (bool, error) {
	var buckets v1alpha1.ObjectStorageBucketList
	if err := r.Client.List(ctx, &buckets, client.InNamespace(user.Namespace)); err != nil {
		return false, err
	}
	for _, bucket := range buckets.Items {
		if bucket.Spec.UserRef == user.Name {
			return true, nil
		}
	}
	return false, nil
}

func (r *UserReconciler) deleteCephUser(ctx context.Context, user *v1alpha1.ObjectStorageUser) error {
	rgw := &unstructured.Unstructured{}
	rgw.SetGroupVersionKind(cephObjectStoreUserGVK)
	rgw.SetNamespace(user.Namespace)
	rgw.SetName(RGWUserName(user.Namespace, user.Name))
	err := r.Client.Delete(ctx, rgw)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	return err
}

func (r *UserReconciler) converge(ctx context.Context, user *v1alpha1.ObjectStorageUser) (outcome, string, error) {
	tenant, project, found, err := projectOf(ctx, r.Client, user.Namespace)
	if err != nil {
		return outcome{}, "", err
	}
	if !found {
		return outcome{reason: reasonNamespaceNotProj, message: "the namespace carries no tenant and project labels", requeue: steadyRequeue}, "", nil
	}
	if problem, err := storeProblem(ctx, r.Client); err != nil || problem.reason != "" {
		return problem, "", err
	}
	desired, err := CephUser(user, tenant, project)
	if errors.Is(err, ErrQuotaInvalid) {
		return outcome{reason: reasonQuotaInvalid, message: err.Error(), requeue: steadyRequeue}, "", nil
	}
	if err != nil {
		return outcome{}, "", err
	}
	if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.ObjectStorageFieldManager); err != nil {
		return outcome{}, "", err
	}
	return r.credentials(ctx, user)
}

func (r *UserReconciler) credentials(ctx context.Context, user *v1alpha1.ObjectStorageUser) (outcome, string, error) {
	rgwName := RGWUserName(user.Namespace, user.Name)
	rgw, found, err := getRook(ctx, r.Client, cephObjectStoreUserGVK, client.ObjectKey{Namespace: user.Namespace, Name: rgwName})
	if err != nil {
		return outcome{}, "", err
	}
	if !found || rookPhase(rgw) != "Ready" {
		return outcome{reason: "UserPending", message: "the object store has not created the user yet", requeue: pendingRequeue}, "", nil
	}
	name := CredentialsSecretName(rgwName)
	var secret corev1.Secret
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: user.Namespace, Name: name}, &secret)
	if client.IgnoreNotFound(err) != nil {
		return outcome{}, "", err
	}
	if err != nil || len(secret.Data[AccessKeyKey]) == 0 || len(secret.Data[SecretKeyKey]) == 0 {
		return outcome{reason: "CredentialsMissing", message: "the credentials Secret " + name + " is missing or incomplete", requeue: pendingRequeue}, "", nil
	}
	return outcome{reason: reasonReady, requeue: steadyRequeue}, name, nil
}

func (r *UserReconciler) report(ctx context.Context, user *v1alpha1.ObjectStorageUser, result outcome, secret string) ctrl.Result {
	err := updateStatus(ctx, r.Client, user, func(latest *v1alpha1.ObjectStorageUser) {
		latest.Status.ObservedGeneration = latest.Generation
		if result.ready() {
			latest.Status.CredentialsSecret = secret
		}
		v1alpha1.SetCondition(&latest.Status.Conditions, condition(v1alpha1.ConditionReady, result.ready(), result.reason, result.message, latest.Generation))
	})
	if err != nil {
		logf.FromContext(ctx).Info("status write failed", "error", err.Error())
	}
	return ctrl.Result{RequeueAfter: result.requeue}
}

func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-user").For(&v1alpha1.ObjectStorageUser{}).Complete(r)
}
