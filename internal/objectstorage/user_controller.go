package objectstorage

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const legacyUserMessage = "this user was made by an older release; delete the ObjectStorageUser and create it again"

var userFinalizer = platform.Finalizer("ObjectStorageUser")

type UserReconciler struct {
	Client   client.Client
	S3       *s3.Client
	Endpoint string
}

type userState struct {
	outcome
	secret string
	arn    string
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
	state, err := r.converge(ctx, &user)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.report(ctx, &user, state), nil
}

func (r *UserReconciler) ensureFinalizer(ctx context.Context, user *v1alpha1.ObjectStorageUser) error {
	if controllerutil.ContainsFinalizer(user, userFinalizer) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageUser{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(user), latest); err != nil {
			return err
		}
		controllerutil.AddFinalizer(latest, userFinalizer)
		if err := r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)); err != nil {
			return err
		}
		*user = *latest
		return nil
	})
}

func (r *UserReconciler) finalize(ctx context.Context, user *v1alpha1.ObjectStorageUser) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(user, userFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.release(ctx, user); err != nil {
		return ctrl.Result{}, err
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageUser{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(user), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		controllerutil.RemoveFinalizer(latest, userFinalizer)
		return client.IgnoreNotFound(r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)))
	})
	return ctrl.Result{}, err
}

func (r *UserReconciler) release(ctx context.Context, user *v1alpha1.ObjectStorageUser) error {
	rgwName := RGWUserName(user.Namespace, user.Name)
	rook, found, err := getRook(ctx, r.Client, cephObjectStoreUserGVK, client.ObjectKey{Namespace: StoreNamespace, Name: rgwName})
	if err != nil {
		return err
	}
	if found && hasAccountRef(rook) {
		r.dropPolicy(ctx, user.Namespace, rgwName)
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, rook)); err != nil {
			return err
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: keysSecretName(user), Namespace: user.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, secret))
}

func (r *UserReconciler) dropPolicy(ctx context.Context, namespace, rgwName string) {
	account, ready, err := ReadAccount(ctx, r.Client, namespace)
	if err != nil || !ready {
		return
	}
	if err := r.S3.DeleteUserPolicy(ctx, r.Endpoint, Region, account.Root, rgwName, PolicyName); err != nil {
		logf.FromContext(ctx).Info("policy delete failed", "user", rgwName, "error", err.Error())
	}
}

func (r *UserReconciler) converge(ctx context.Context, user *v1alpha1.ObjectStorageUser) (userState, error) {
	project, found, err := LoadProject(ctx, r.Client, user.Namespace)
	if err != nil {
		return userState{}, err
	}
	if !found {
		return userBlocked(reasonNamespaceNotProj, "the namespace carries no tenant and project labels", steadyRequeue), nil
	}
	if problem, err := storeProblem(ctx, r.Client); err != nil || problem.reason != "" {
		return userState{outcome: problem}, err
	}
	if legacy, err := r.isLegacy(ctx, user); err != nil || legacy {
		return userBlocked(reasonLegacyUser, legacyUserMessage, steadyRequeue), err
	}
	account, ready, err := EnsureAccount(ctx, r.Client, project)
	if err != nil || !ready {
		return userState{outcome: accountPending()}, err
	}
	return r.converged(ctx, user, project, account)
}

func (r *UserReconciler) converged(ctx context.Context, user *v1alpha1.ObjectStorageUser, project Project, account Account) (userState, error) {
	if err := ssa.Apply(ctx, r.Client, CephUser(user, project), v1alpha1.ObjectStorageFieldManager); err != nil {
		return userState{}, err
	}
	rgwName := RGWUserName(user.Namespace, user.Name)
	rook, found, err := getRook(ctx, r.Client, cephObjectStoreUserGVK, client.ObjectKey{Namespace: StoreNamespace, Name: rgwName})
	if err != nil {
		return userState{}, err
	}
	if found && !user.Spec.Suspended {
		if err := r.clearMask(ctx, rook); err != nil {
			return userState{}, err
		}
	}
	if !found || rookPhase(rook) != "Ready" {
		return userBlocked("UserPending", "the object store has not created the user yet", pendingRequeue), nil
	}
	keys, complete, err := rookKeys(ctx, r.Client, rgwName)
	if err != nil {
		return userState{}, err
	}
	if !complete {
		return userBlocked("CredentialsMissing", "the credentials Secret "+CredentialsSecretName(rgwName)+" is missing or incomplete", pendingRequeue), nil
	}
	if err := ssa.Apply(ctx, r.Client, keysSecret(user, project, keys, r.Endpoint), v1alpha1.ObjectStorageFieldManager); err != nil {
		return userState{}, err
	}
	state := userState{secret: keysSecretName(user), arn: UserARN(account.ID, rgwName)}
	state.outcome, err = r.applyPolicy(ctx, user, account, rgwName)
	return state, err
}

func (r *UserReconciler) clearMask(ctx context.Context, rook *unstructured.Unstructured) error {
	if _, present, _ := unstructured.NestedSlice(rook.Object, "spec", "opMask"); !present {
		return nil
	}
	patch := []byte(`{"spec":{"opMask":null}}`)
	return r.Client.Patch(ctx, rook, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(v1alpha1.ObjectStorageFieldManager))
}

func userBlocked(reason, message string, requeue time.Duration) userState {
	return userState{outcome: outcome{reason: reason, message: message, requeue: requeue}}
}

func (r *UserReconciler) isLegacy(ctx context.Context, user *v1alpha1.ObjectStorageUser) (bool, error) {
	if user.Spec.Permission == "" {
		return true, nil
	}
	rgwName := RGWUserName(user.Namespace, user.Name)
	_, old, err := getRook(ctx, r.Client, cephObjectStoreUserGVK, client.ObjectKey{Namespace: user.Namespace, Name: rgwName})
	if err != nil || old {
		return old, err
	}
	current, found, err := getRook(ctx, r.Client, cephObjectStoreUserGVK, client.ObjectKey{Namespace: StoreNamespace, Name: rgwName})
	if err != nil || !found {
		return false, err
	}
	return !hasAccountRef(current), nil
}

func (r *UserReconciler) applyPolicy(ctx context.Context, user *v1alpha1.ObjectStorageUser, account Account, rgwName string) (outcome, error) {
	scope, err := bucketsInScope(ctx, r.Client, user)
	if err != nil {
		return outcome{}, err
	}
	wanted, err := UserPolicy(user.Spec.Permission, scope)
	if err != nil {
		return outcome{reason: reasonPolicyFailed, message: err.Error(), requeue: steadyRequeue}, nil
	}
	stored, found, callErr := r.S3.GetUserPolicy(ctx, r.Endpoint, Region, account.Root, rgwName, PolicyName)
	if callErr == nil && (!found || !samePolicy(stored, wanted)) {
		callErr = r.S3.PutUserPolicy(ctx, r.Endpoint, Region, account.Root, rgwName, PolicyName, wanted)
	}
	if callErr != nil {
		return outcome{reason: failureReason(callErr, reasonPolicyFailed), message: callErr.Error(), requeue: pendingRequeue}, nil
	}
	return outcome{reason: reasonReady, requeue: steadyRequeue}, nil
}

func (r *UserReconciler) report(ctx context.Context, user *v1alpha1.ObjectStorageUser, state userState) ctrl.Result {
	err := updateStatus(ctx, r.Client, user, func(latest *v1alpha1.ObjectStorageUser) {
		latest.Status.ObservedGeneration = latest.Generation
		if state.secret != "" {
			latest.Status.CredentialsSecret = state.secret
			latest.Status.ARN = state.arn
		}
		v1alpha1.SetCondition(&latest.Status.Conditions, condition(v1alpha1.ConditionReady, state.ready(), state.reason, state.message, latest.Generation))
	})
	if err != nil {
		logf.FromContext(ctx).Info("status write failed", "error", err.Error())
	}
	return ctrl.Result{RequeueAfter: state.requeue}
}

func (r *UserReconciler) usersOfBucket(ctx context.Context, obj client.Object) []reconcile.Request {
	var users v1alpha1.ObjectStorageUserList
	if err := r.Client.List(ctx, &users, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(users.Items))
	for _, user := range users.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&user)})
	}
	return requests
}

func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-user").
		For(&v1alpha1.ObjectStorageUser{}).
		Watches(&v1alpha1.ObjectStorageBucket{}, handler.EnqueueRequestsFromMapFunc(r.usersOfBucket)).
		Complete(r)
}
