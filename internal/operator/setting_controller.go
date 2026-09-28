package operator

import (
	"context"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

type SettingReconciler struct {
	Client client.Client
}

func (r *SettingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var setting v1alpha1.Setting
	if err := r.Client.Get(ctx, req.NamespacedName, &setting); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	next := settingStatus(setting)
	if statusEqual(setting.Status, next) {
		return ctrl.Result{}, nil
	}
	setting.Status = next
	return ctrl.Result{}, r.Client.Status().Update(ctx, &setting)
}

func settingStatus(setting v1alpha1.Setting) v1alpha1.SettingStatus {
	status := setting.Status
	status.Conditions = slices.Clone(setting.Status.Conditions)
	status.ObservedGeneration = setting.Generation
	def, known := settings.Lookup(setting.Name)
	if !known {
		status.Default = ""
		status.Applied = ""
		v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "UnknownKey", Message: "no such setting in the catalog", ObservedGeneration: setting.Generation})
		return status
	}
	status.Default = def.Default
	if err := def.Validate(setting.Spec.Value); err != nil {
		status.Applied = ""
		v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "Invalid", Message: err.Error(), ObservedGeneration: setting.Generation})
		return status
	}
	status.Applied = effectiveValue(setting.Spec.Value, def.Default)
	v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Valid", ObservedGeneration: setting.Generation})
	return status
}

func effectiveValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func statusEqual(a, b v1alpha1.SettingStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration || a.Default != b.Default || a.Applied != b.Applied || len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		if a.Conditions[i].Type != b.Conditions[i].Type || a.Conditions[i].Status != b.Conditions[i].Status || a.Conditions[i].Reason != b.Conditions[i].Reason || a.Conditions[i].Message != b.Conditions[i].Message {
			return false
		}
	}
	return true
}

func (r *SettingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Setting{}).Complete(r)
}
