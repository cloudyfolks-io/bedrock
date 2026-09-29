package housekeeping

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func holder(c client.Client) string {
	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), objectKey(LeaseName), &lease); err != nil || lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

func signingKeys(t *testing.T, c client.Client) int {
	t.Helper()
	var list v1alpha1.SigningKeyList
	if err := c.List(context.Background(), &list, client.InNamespace("bedrock-system")); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func runner(ctx context.Context, cfg *rest.Config, c client.Client, identity string) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, c, identity, 200*time.Millisecond)
	}()
	return done
}

func TestRunOnlyOnLeader(t *testing.T) {
	c, cfg := startTestEnv(t)
	ctx := context.Background()
	ctxA, cancelA := context.WithCancel(ctx)
	t.Cleanup(cancelA)
	doneA := runner(ctxA, cfg, c, "replica-a")
	eventually(t, "replica-a to lead and create the first key", func() bool {
		return holder(c) == "replica-a" && signingKeys(t, c) == 1
	})

	ctxB, cancelB := context.WithCancel(ctx)
	t.Cleanup(cancelB)
	doneB := runner(ctxB, cfg, c, "replica-b")
	for range 30 {
		if got := holder(c); got != "replica-a" {
			t.Fatalf("the lease moved to %q while replica-a runs", got)
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancelA()
	if err := <-doneA; err != nil {
		t.Fatalf("Run must return nil when its context ends: %v", err)
	}
	eventually(t, "replica-b to take the lease", func() bool {
		return holder(c) == "replica-b"
	})
	stale := authRequest("stale-request", metav1.NewTime(time.Now().Add(-time.Minute)))
	create(t, c, stale)
	eventually(t, "replica-b to sweep", func() bool {
		return apierrors.IsNotFound(c.Get(ctx, objectKey(stale.Name), &v1alpha1.AuthRequest{}))
	})
	if got := signingKeys(t, c); got != 1 {
		t.Fatalf("two replicas must still share one key, got %d", got)
	}
	cancelB()
	if err := <-doneB; err != nil {
		t.Fatalf("Run must return nil when its context ends: %v", err)
	}
}
