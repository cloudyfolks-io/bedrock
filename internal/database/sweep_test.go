package database

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func engineSecret(name, instance, ownerName, ownerNamespace, ownerUID string) *corev1.Secret {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: PlatformNamespace,
		Labels: map[string]string{
			v1alpha1.LabelKind: Kind, v1alpha1.LabelName: ownerName, v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web",
			LabelManagedBy: ManagedBy, LabelInstance: instance,
		},
	}}
	if ownerUID != "" {
		secret.Annotations = map[string]string{platform.AnnotationOwnerUID: ownerUID, platform.AnnotationOwnerNamespace: ownerNamespace}
	}
	return secret
}

func exists(c client.Client, obj client.Object) bool {
	err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)
	return !apierrors.IsNotFound(err)
}

func TestSweeperRemovesOnlyOrphansAndOnlyWhenTheDryRunIsOff(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	setSetting(t, c, "database.enabled", "true")
	cfg, err := LoadConfig(ctx, c)
	must(t, err)
	must(t, EnsurePlatform(ctx, c, cfg))

	namespace := projectNamespace(t, c)
	live := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: namespace}, Spec: newSpec()}
	create(t, c, live)

	orphan := engineSecret("orphan-"+nextSequence(), "dead-1", "gone", namespace, "dead-uid-1")
	kept := engineSecret("kept-"+nextSequence(), string(live.UID), "live", namespace, string(live.UID))
	reused := engineSecret("reused-"+nextSequence(), "dead-2", "live", namespace, "uid-of-an-older-live")
	foreign := engineSecret("foreign-"+nextSequence(), "x", "tenant-object", namespace, "")
	for _, secret := range []*corev1.Secret{orphan, kept, reused, foreign} {
		create(t, c, secret)
	}

	sweeper := &Sweeper{Client: c}
	found, err := sweeper.Sweep(ctx)
	must(t, err)
	names := map[string]bool{}
	for _, obj := range found {
		names[obj.GetName()] = true
	}
	if !names[orphan.Name] || !names[reused.Name] || names[kept.Name] || names[foreign.Name] {
		t.Fatalf("found %v", names)
	}
	for _, secret := range []*corev1.Secret{orphan, kept, reused, foreign} {
		if !exists(c, secret) {
			t.Fatalf("the dry run removed %s", secret.Name)
		}
	}

	setSetting(t, c, "database.sweeper-dry-run", "false")
	_, err = sweeper.Sweep(ctx)
	must(t, err)
	if exists(c, orphan) || exists(c, reused) {
		t.Fatal("an orphan survived the sweep")
	}
	if !exists(c, kept) || !exists(c, foreign) {
		t.Fatal("the sweep removed an object that has an owner or no owner mark")
	}
}

func TestSweeperDoesNothingWhileTheDatabaseKindsAreOff(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	setSetting(t, c, "database.enabled", "false")
	setSetting(t, c, "database.sweeper-dry-run", "false")
	must(t, EnsurePlatform(ctx, c, Config{CapacityCPU: mustQuantity("1"), CapacityMemory: mustQuantity("1Gi"), CapacityPods: 1}))
	orphan := engineSecret("orphan-off-"+nextSequence(), "dead", "gone", "x", "dead-uid")
	create(t, c, orphan)
	found, err := (&Sweeper{Client: c}).Sweep(ctx)
	must(t, err)
	if len(found) != 0 || !exists(c, orphan) {
		t.Fatalf("the sweeper acted while disabled: %v", found)
	}
}
