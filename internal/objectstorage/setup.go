package objectstorage

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/admin"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

func SetupWithManager(mgr ctrl.Manager) error {
	users := &UserReconciler{Client: mgr.GetClient(), S3: &s3.Client{}, Endpoint: Endpoint}
	if err := users.SetupWithManager(mgr); err != nil {
		return err
	}
	namespaces := &NamespaceReconciler{Client: mgr.GetClient()}
	if err := namespaces.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(&Sweeper{Client: mgr.GetClient(), DryRun: true}); err != nil {
		return err
	}
	rules := &RulesConfigReconciler{Client: mgr.GetClient()}
	if err := rules.SetupWithManager(mgr); err != nil {
		return err
	}
	loadRules := func(ctx context.Context) (Rules, error) { return LoadRules(ctx, mgr.GetClient()) }
	buckets := &BucketReconciler{Client: mgr.GetClient(), S3: &s3.Client{}, Admin: &admin.Client{}, Endpoint: Endpoint, Rules: loadRules}
	return buckets.SetupWithManager(mgr)
}
