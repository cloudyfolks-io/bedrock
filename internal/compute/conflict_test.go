package compute

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func conflictOnce(base client.WithWatch) (client.Client, *int) {
	calls := 0
	wrapped := interceptor.NewClient(base, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			calls++
			if calls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "objects"}, obj.GetName(), errors.New("the object was modified"))
			}
			return c.SubResource(subResource).Update(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			calls++
			if calls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "objects"}, obj.GetName(), errors.New("the object was modified"))
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	return wrapped, &calls
}

func watchClient(t *testing.T) (client.WithWatch, client.Client) {
	t.Helper()
	_, cfg := startEnv(t)
	base, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	must(t, err)
	return base, base
}

func TestVMStatusWriteRetriesAfterAConflict(t *testing.T) {
	base, plain := watchClient(t)
	ns := seedPlatform(t, plain)
	createVM(t, plain, ns, "web-1", noMutation)
	wrapped, calls := conflictOnce(base)
	reconcileVM(t, newVMReconciler(wrapped), ns, "web-1")
	if *calls < 2 || readyOf(getVM(t, plain, ns, "web-1")) == nil {
		t.Fatalf("the status was not written after the conflict (calls %d)", *calls)
	}
}

func TestVMStatusWriteFailureIsReturned(t *testing.T) {
	base, plain := watchClient(t)
	ns := seedPlatform(t, plain)
	createVM(t, plain, ns, "web-1", noMutation)
	failing := interceptor.NewClient(base, interceptor.Funcs{
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return apierrors.NewServiceUnavailable("down")
		},
	})
	_, err := newVMReconciler(failing).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: "web-1"}})
	if err == nil {
		t.Fatal("a lost status write must requeue the request")
	}
}

func TestVolumeWritesRetryAfterAConflict(t *testing.T) {
	base, plain := watchClient(t)
	ns := uniqueNamespace(t, plain)
	newVolume(t, plain, ns, "data1", func(v *v1alpha1.Volume) {})
	wrapped, calls := conflictOnce(base)
	reconcileVolume(t, wrapped, ns, "data1")
	var volume v1alpha1.Volume
	must(t, plain.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &volume))
	if *calls < 2 || !containsString(volume.Finalizers, FinalizerVolumeInUse) {
		t.Fatalf("the finalizer was lost (calls %d)", *calls)
	}
	setDataVolumePhase(t, plain, ns, "data1", "Succeeded")
	wrapped, calls = conflictOnce(base)
	reconcileVolume(t, wrapped, ns, "data1")
	must(t, plain.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data1"}, &volume))
	if *calls < 2 || volume.Status.Phase != v1alpha1.VolumePhaseAvailable {
		t.Fatalf("the status was lost (calls %d)", *calls)
	}
}

func TestVolumeSnapshotStatusWriteRetriesAfterAConflict(t *testing.T) {
	base, plain := watchClient(t)
	ns, r := snapshotFixture(t, plain, "block", rbdProvisioner, nil)
	wrapped, calls := conflictOnce(base)
	r.Client = wrapped
	reconcileSnapshot(t, r, ns)
	if *calls < 2 {
		t.Fatalf("calls %d", *calls)
	}
	requireSnapshotReason(t, plain, ns, "Pending")
}

func TestSSHKeyStatusWriteRetriesAfterAConflict(t *testing.T) {
	base, plain := watchClient(t)
	key := newSSHKey(t, plain, ed25519Key(t))
	wrapped, calls := conflictOnce(base)
	got := reconcileSSHKey(t, wrapped, key)
	if *calls < 2 || meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady) == nil {
		t.Fatalf("the status was lost (calls %d)", *calls)
	}
}

func TestVMDoesNotPruneSnapshotsDuringARestore(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	createObject(t, c, snapshotObject(ns, "past", "web-1", map[string]string{AnnotationExpiresAt: fixedNow.Add(-time.Hour).Format(time.RFC3339)}))
	vm := getVM(t, c, ns, "web-1")
	vm.Annotations = map[string]string{AnnotationRestoreHold: "restore-1"}
	must(t, c.Update(context.Background(), &vm))
	reconcileVM(t, r, ns, "web-1")
	requirePresent(t, c, vmSnapshotGVK, ns, "past")
	vm = getVM(t, c, ns, "web-1")
	vm.Annotations = nil
	must(t, c.Update(context.Background(), &vm))
	reconcileVM(t, r, ns, "web-1")
	requireAbsent(t, c, vmSnapshotGVK, ns, "past")
}

func TestCreateRaceChecksTheOwner(t *testing.T) {
	base, plain := watchClient(t)
	ns := uniqueNamespace(t, plain)
	racing := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			foreign := obj.DeepCopyObject().(client.Object)
			foreign.SetOwnerReferences(nil)
			if err := c.Create(ctx, foreign); err != nil {
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "datavolumes"}, obj.GetName())
		},
	})
	vm := fixtureVM(ns, "web-1")
	vm.UID = "vm-uid"
	claim := RootClaim(vm, map[string]string{}, nil)
	err := createIfAbsent(context.Background(), racing, claim)
	var conflict nameConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v, want a name conflict", err)
	}
	if refs := requirePresent(t, plain, dataVolumeGVK, ns, "web-1-root").GetOwnerReferences(); len(refs) != 0 {
		t.Fatalf("owner %+v", refs)
	}
}
