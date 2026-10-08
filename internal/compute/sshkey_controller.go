package compute

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ReasonValid            = "Valid"
	ReasonInvalidPublicKey = "InvalidPublicKey"
)

type SSHKeyReconciler struct {
	Client client.Client
}

func sshKeyCondition(key v1alpha1.SSHKey) metav1.Condition {
	cond := metav1.Condition{Type: v1alpha1.ConditionReady, ObservedGeneration: key.Generation}
	if _, err := AuthorizedKey(key.Spec.PublicKey); err != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, ReasonInvalidPublicKey, err.Error()
		return cond
	}
	cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, ReasonValid, "the public key is valid"
	return cond
}

func (r *SSHKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var key v1alpha1.SSHKey
		if err := r.Client.Get(ctx, req.NamespacedName, &key); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := key.DeepCopy()
		desired.Status.ObservedGeneration = key.Generation
		v1alpha1.SetCondition(&desired.Status.Conditions, sshKeyCondition(key))
		if equality.Semantic.DeepEqual(key.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ComputeFieldManager))
	})
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, err
}

func (r *SSHKeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("compute-sshkey").For(&v1alpha1.SSHKey{}).Complete(r)
}
