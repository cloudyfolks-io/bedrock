package objectstorage

import (
	ctrl "sigs.k8s.io/controller-runtime"

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
	buckets := &BucketReconciler{Client: mgr.GetClient(), S3: &s3.Client{}, Endpoint: Endpoint}
	return buckets.SetupWithManager(mgr)
}
