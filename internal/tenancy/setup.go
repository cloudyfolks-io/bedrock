package tenancy

import (
	ctrl "sigs.k8s.io/controller-runtime"
)

func SetupWithManager(mgr ctrl.Manager) error {
	tenants := &TenantReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}
	if err := tenants.SetupWithManager(mgr); err != nil {
		return err
	}
	projects := &ProjectReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}
	return projects.SetupWithManager(mgr)
}
