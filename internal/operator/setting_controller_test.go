package operator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func TestSettingReconcilerSeedsAndValidates(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&SettingReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	if err := settings.Seed(ctx, c); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		var s v1alpha1.Setting
		if err := c.Get(ctx, client.ObjectKey{Name: "storage.replicas"}, &s); err != nil {
			return false
		}
		return s.Status.Default == "1" && v1alpha1.IsConditionTrue(s.Status.Conditions, v1alpha1.ConditionReady)
	})

	var s v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: "storage.replicas"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Value = "0"
	if err := c.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Setting
		if err := c.Get(ctx, client.ObjectKey{Name: "storage.replicas"}, &got); err != nil {
			return false
		}
		return !v1alpha1.IsConditionTrue(got.Status.Conditions, v1alpha1.ConditionReady) && got.Status.ObservedGeneration == got.Generation
	})

	unknown := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "nope.key"}}
	if err := c.Create(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Setting
		if err := c.Get(ctx, client.ObjectKey{Name: "nope.key"}, &got); err != nil {
			return false
		}
		return len(got.Status.Conditions) == 1 && got.Status.Conditions[0].Reason == "UnknownKey"
	})
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met within 10s")
}
