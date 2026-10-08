package objectstorage

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSetupRegistersTheControllers(t *testing.T) {
	fresh(t)
	mgr, err := ctrl.NewManager(testConfig, ctrl.Options{
		Scheme:     testScheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		var user v1alpha1.ObjectStorageUser
		if err := testClient.Get(ctx, client.ObjectKey{Namespace: "acme-prod", Name: "app"}, &user); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return controllerutil.ContainsFinalizer(&user, UserFinalizer), nil
	})
	if err != nil {
		t.Fatalf("the user never got its finalizer: %v", err)
	}
}
