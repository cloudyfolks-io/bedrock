package network

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

var sweepEpoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func ownedObject(u *unstructured.Unstructured, ownerKind, namespace, name, uid string, created time.Time) *unstructured.Unstructured {
	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelManaged] = "true"
	labels[v1alpha1.LabelKind] = ownerKind
	u.SetLabels(labels)
	u.SetAnnotations(map[string]string{AnnotationOwnerNamespace: namespace, AnnotationOwnerName: name, AnnotationOwnerUID: uid})
	u.SetCreationTimestamp(metav1.NewTime(created))
	return u
}

func eipOf(name, owner, uid string, created time.Time) *unstructured.Unstructured {
	return ownedObject(fabric.OvnEip(fabric.Meta{Name: name}, "provider-public", ""), "ElasticIP", "ns", owner, uid, created)
}

func fipOf(name, owner, uid string, created time.Time) *unstructured.Unstructured {
	return ownedObject(fabric.OvnFip(fabric.Meta{Name: name}, "e", "ip"), "ElasticIP", "ns", owner, uid, created)
}

func ownersOf(entries ...OwnerKey) map[OwnerKey]types.UID {
	owners := map[OwnerKey]types.UID{}
	for _, e := range entries {
		owners[e] = types.UID(e.Name + "-uid")
	}
	return owners
}

func TestFindOrphansReturnsObjectsWithoutOwner(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	got := FindOrphans([]*unstructured.Unstructured{eipOf("eip", "gone", "u", old)}, ownersOf(), sweepEpoch, time.Minute)
	if len(got) != 1 || got[0].GVK != fabric.OvnEipGVK || got[0].Name != "eip" {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansKeepsObjectsWithLiveOwner(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	owners := ownersOf(OwnerKey{Kind: "ElasticIP", Namespace: "ns", Name: "live"})
	if got := FindOrphans([]*unstructured.Unstructured{eipOf("eip", "live", "live-uid", old)}, owners, sweepEpoch, time.Minute); len(got) != 0 {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansTreatsANewUIDAsOrphan(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	owners := ownersOf(OwnerKey{Kind: "ElasticIP", Namespace: "ns", Name: "again"})
	if got := FindOrphans([]*unstructured.Unstructured{eipOf("eip", "again", "old-uid", old)}, owners, sweepEpoch, time.Minute); len(got) != 1 {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansSeparatesOwnerKindsWithTheSameName(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	subnet := ownedObject(fabric.Subnet(fabric.Meta{Name: "s"}, fabric.SubnetSpec{CIDR: "10.0.0.0/24", Gateway: "10.0.0.1"}), "Subnet", "ns", "default", "default-uid", old)
	group := ownedObject(fabric.SecurityGroup(fabric.Meta{Name: "g"}, nil, nil), "SecurityGroup", "ns", "default", "group-uid", old)
	owners := map[OwnerKey]types.UID{
		{Kind: "Subnet", Namespace: "ns", Name: "default"}:        "default-uid",
		{Kind: "SecurityGroup", Namespace: "ns", Name: "default"}: "group-uid",
	}
	if got := FindOrphans([]*unstructured.Unstructured{subnet, group}, owners, sweepEpoch, time.Minute); len(got) != 0 {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansIgnoresYoungObjects(t *testing.T) {
	young := sweepEpoch.Add(-10 * time.Second)
	if got := FindOrphans([]*unstructured.Unstructured{eipOf("eip", "gone", "u", young)}, ownersOf(), sweepEpoch, time.Minute); len(got) != 0 {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansIgnoresUnmanagedAndUnderlayObjects(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	unmanaged := eipOf("plain", "gone", "u", old)
	unmanaged.SetLabels(map[string]string{})
	underlay := eipOf("underlay", "gone", "u", old)
	labels := underlay.GetLabels()
	labels[LabelComponent] = ComponentUnderlay
	underlay.SetLabels(labels)
	noOwner := eipOf("noowner", "gone", "u", old)
	noOwner.SetAnnotations(nil)
	if got := FindOrphans([]*unstructured.Unstructured{unmanaged, underlay, noOwner}, ownersOf(), sweepEpoch, time.Minute); len(got) != 0 {
		t.Fatalf("orphans %+v", got)
	}
}

func TestFindOrphansOrdersFipsBeforeEips(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	vpc := ownedObject(fabric.Vpc(fabric.Meta{Name: "v"}, fabric.VpcSpec{}), "Network", "", "net", "u", old)
	got := FindOrphans([]*unstructured.Unstructured{vpc, eipOf("eip", "gone", "u", old), fipOf("fip", "gone", "u", old)}, ownersOf(), sweepEpoch, time.Minute)
	if len(got) != 3 || got[0].GVK != fabric.OvnFipGVK || got[1].GVK != fabric.OvnEipGVK || got[2].GVK != fabric.VpcGVK {
		t.Fatalf("orphans %+v", got)
	}
}

func orphanFixture(t *testing.T, c client.Client, owner, uid string) (eip, fip *unstructured.Unstructured) {
	t.Helper()
	ctx := context.Background()
	eip = eipOf(unique(t, "orphan-eip"), owner, uid, time.Now())
	fip = fipOf(unique(t, "orphan-fip"), owner, uid, time.Now())
	for _, u := range []*unstructured.Unstructured{eip, fip} {
		u.SetCreationTimestamp(metav1.Time{})
		if err := c.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	return eip, fip
}

func TestSweeperDeletesObjectsWithoutOwner(t *testing.T) {
	c := cleanEnv(t)
	eip, fip := orphanFixture(t, c, "deleted", "old-uid")
	sweeper := &Sweeper{Client: c, Now: func() time.Time { return time.Now().Add(2 * time.Minute) }}
	if err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*unstructured.Unstructured{eip, fip} {
		if _, err := getObject(context.Background(), c, u.GroupVersionKind(), u.GetName()); !apierrors.IsNotFound(err) {
			t.Fatalf("%s must be deleted: %v", u.GetKind(), err)
		}
	}
}

func TestSweeperKeepsObjectsWithLiveOwner(t *testing.T) {
	c := cleanEnv(t)
	ns := newNamespace(t, unique(t, "acme"), "prod")
	live := elasticIPOf(ns, "live", "")
	requireAccepted(t, c, live)
	eip, _ := orphanFixture(t, c, "live", string(live.UID))
	eip.SetAnnotations(map[string]string{AnnotationOwnerNamespace: ns, AnnotationOwnerName: "live", AnnotationOwnerUID: string(live.UID)})
	if err := c.Update(context.Background(), eip); err != nil {
		t.Fatal(err)
	}
	sweeper := &Sweeper{Client: c, Now: func() time.Time { return time.Now().Add(2 * time.Minute) }}
	if err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := getObject(context.Background(), c, fabric.OvnEipGVK, eip.GetName()); err != nil {
		t.Fatalf("an object with a live owner must stay: %v", err)
	}
}

func TestSweeperContinuesAfterARefusedDelete(t *testing.T) {
	c := cleanEnv(t)
	_, cfg := testEnv(t)
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	first, second := orphanFixture(t, c, "deleted", "old-uid")
	refusing := interceptor.NewClient(watching, interceptor.Funcs{Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if obj.GetName() == second.GetName() {
			return errors.New("fabric refuses")
		}
		return cl.Delete(ctx, obj, opts...)
	}})
	sweeper := &Sweeper{Client: refusing, Now: func() time.Time { return time.Now().Add(2 * time.Minute) }}
	if err := sweeper.SweepOnce(context.Background()); err == nil {
		t.Fatal("the refused delete must be reported")
	}
	if _, err := getObject(context.Background(), c, fabric.OvnEipGVK, first.GetName()); !apierrors.IsNotFound(err) {
		t.Fatalf("the other orphan must still be deleted: %v", err)
	}
}

func defaultGroupOf(namespace string, created time.Time) *unstructured.Unstructured {
	group := DefaultSecurityGroup(namespace, ObjectLabels("Namespace", namespace, "t1", "prod"))
	group.SetCreationTimestamp(metav1.NewTime(created))
	return group
}

func TestFindOrphansSweepsTheDefaultGroupOfAGoneNamespace(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	groups := []*unstructured.Unstructured{defaultGroupOf("gone", old), defaultGroupOf("alive", old)}
	owners := map[OwnerKey]types.UID{{Kind: "Namespace", Name: "alive"}: "uid"}
	got := FindOrphans(groups, owners, sweepEpoch, time.Minute)
	if len(got) != 1 || got[0].Name != DefaultSecurityGroupName("gone") {
		t.Fatalf("orphans %+v", got)
	}
}

func watchingClient(t *testing.T, funcs interceptor.Funcs) client.Client {
	t.Helper()
	_, cfg := testEnv(t)
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return interceptor.NewClient(watching, funcs)
}

func afterTwoMinutes() time.Time { return time.Now().Add(2 * time.Minute) }

func TestSweeperDeletesTheDefaultGroupOfAGoneNamespace(t *testing.T) {
	c := cleanEnv(t)
	alive := newNamespace(t, unique(t, "acme"), "prod")
	gone := unique(t, "gone")
	for _, ns := range []string{alive, gone} {
		group := defaultGroupOf(ns, time.Now())
		group.SetCreationTimestamp(metav1.Time{})
		if err := c.Create(context.Background(), group); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Sweeper{Client: c, Now: afterTwoMinutes}).SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, DefaultSecurityGroupName(gone)); !apierrors.IsNotFound(err) {
		t.Fatalf("the group of a gone namespace must be deleted: %v", err)
	}
	if _, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, DefaultSecurityGroupName(alive)); err != nil {
		t.Fatalf("the group of a live namespace must stay: %v", err)
	}
}

func TestSweeperDeletesNothingWhenAListFails(t *testing.T) {
	c := cleanEnv(t)
	eip, fip := orphanFixture(t, c, "deleted", "old-uid")
	failing := watchingClient(t, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, isGroups := list.(*v1alpha1.SecurityGroupList); isGroups {
			return errors.New("the API server is unavailable")
		}
		return cl.List(ctx, list, opts...)
	}})
	if err := (&Sweeper{Client: failing, Now: afterTwoMinutes}).SweepOnce(context.Background()); err == nil {
		t.Fatal("the failure must be reported")
	}
	for _, u := range []*unstructured.Unstructured{eip, fip} {
		if _, err := getObject(context.Background(), c, u.GroupVersionKind(), u.GetName()); err != nil {
			t.Fatalf("%s must stay: %v", u.GetKind(), err)
		}
	}
}

func TestSweeperConfirmsTheOwnerWithALiveRead(t *testing.T) {
	c := cleanEnv(t)
	ns := newNamespace(t, unique(t, "acme"), "prod")
	live := elasticIPOf(ns, "live", "")
	requireAccepted(t, c, live)
	kept, _ := orphanFixture(t, c, "live", string(live.UID))
	kept.SetAnnotations(map[string]string{AnnotationOwnerNamespace: ns, AnnotationOwnerName: "live", AnnotationOwnerUID: string(live.UID)})
	if err := c.Update(context.Background(), kept); err != nil {
		t.Fatal(err)
	}
	dropped, _ := orphanFixture(t, c, "deleted", "old-uid")
	stale := watchingClient(t, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, isEIPs := list.(*v1alpha1.ElasticIPList); isEIPs {
			return nil
		}
		return cl.List(ctx, list, opts...)
	}})
	sweeper := &Sweeper{Client: stale, Reader: c, Now: afterTwoMinutes}
	if err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := getObject(context.Background(), c, fabric.OvnEipGVK, kept.GetName()); err != nil {
		t.Fatalf("a stale list must not delete an object with a live owner: %v", err)
	}
	if _, err := getObject(context.Background(), c, fabric.OvnEipGVK, dropped.GetName()); !apierrors.IsNotFound(err) {
		t.Fatalf("an object whose owner is gone must be deleted: %v", err)
	}
}
