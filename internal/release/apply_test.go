package release

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestInventoryOf(t *testing.T) {
	bundle, err := Load(os.DirFS("testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	inv := InventoryOf(bundle.Groups)
	if len(inv) != 4 {
		t.Fatalf("inventory size %d", len(inv))
	}
	if _, ok := inv[ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "release-test", Name: "alpha"}]; !ok {
		t.Fatal("alpha missing from inventory")
	}
}

func TestApplyThenPrune(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	bundle, err := Load(os.DirFS("testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	applier := Applier{Client: c}
	for _, group := range bundle.Groups {
		if err := applier.Apply(ctx, group); err != nil {
			t.Fatalf("apply %s: %v", group.Name, err)
		}
	}
	var alpha corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err != nil {
		t.Fatal(err)
	}
	if alpha.Data["key"] != "one" || alpha.Labels[ComponentLabel] != "bedrock" {
		t.Fatalf("alpha %+v", alpha)
	}
	if len(alpha.ManagedFields) == 0 || alpha.ManagedFields[0].Manager != FieldManager {
		t.Fatalf("field manager %+v", alpha.ManagedFields)
	}

	if err := applier.Apply(ctx, bundle.Groups[1]); err != nil {
		t.Fatalf("second apply must be idempotent: %v", err)
	}

	current := InventoryOf(bundle.Groups)
	if err := WriteInventory(ctx, c, current); err != nil {
		t.Fatal(err)
	}
	stored, err := ReadInventory(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(current) {
		t.Fatalf("stored %d current %d", len(stored), len(current))
	}

	next := Inventory{}
	for ref := range current {
		if ref.Name != "beta" {
			next[ref] = struct{}{}
		}
	}
	if err := applier.Prune(ctx, current, next); err != nil {
		t.Fatal(err)
	}
	var beta corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "beta"}, &beta); !errors.IsNotFound(err) {
		t.Fatalf("beta must be pruned, got %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err != nil {
		t.Fatalf("alpha must survive: %v", err)
	}

	empty := Inventory{}
	if err := applier.Prune(ctx, current, empty); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: "release-test"}, &ns); err != nil {
		t.Fatalf("namespace must never be pruned: %v", err)
	}
}

func TestPruneIgnoresVanishedAPI(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	applier := Applier{Client: c}
	previous := Inventory{ObjectRef{APIVersion: "gone.example.com/v1", Kind: "Ghost", Name: "phantom"}: struct{}{}}
	if err := applier.Prune(ctx, previous, Inventory{}); err != nil {
		t.Fatalf("prune must ignore a vanished api group: %v", err)
	}
}

func TestInstallAppliesAllGroupsAndPrunes(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	bundle, err := Load(os.DirFS("testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	var reported []string
	report := func(group Group, err error) { reported = append(reported, group.Name) }
	if err := Install(ctx, c, bundle, nil, Gates{}, 200*time.Millisecond, 0, report); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 2 || reported[0] != "crds" || reported[1] != "bedrock" {
		t.Fatalf("reported %v", reported)
	}
	smaller := bundle
	smaller.Groups = []Group{bundle.Groups[0], {Order: 90, Name: "bedrock", Objects: bundle.Groups[1].Objects[:2]}}
	if err := Install(ctx, c, smaller, nil, Gates{}, 200*time.Millisecond, 0, report); err != nil {
		t.Fatal(err)
	}
	var beta corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "beta"}, &beta); !errors.IsNotFound(err) {
		t.Fatalf("beta must be pruned by the second install, got %v", err)
	}
}

func TestInstallGroupsRunsTheHookAfterEachGate(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	bundle, err := Load(os.DirFS("testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	var events []string
	report := func(group Group, err error) { events = append(events, "report "+group.Name) }
	after := func(ctx context.Context, group Group) error {
		var alpha corev1.ConfigMap
		if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); group.Name == "bedrock" && err != nil {
			return err
		}
		if _, ok := ctx.Deadline(); !ok {
			return errors.NewBadRequest("the hook must run inside the group timeout")
		}
		events = append(events, "after "+group.Name)
		return nil
	}
	if err := InstallGroups(ctx, c, bundle, nil, Gates{}, 200*time.Millisecond, time.Minute, report, after); err != nil {
		t.Fatal(err)
	}
	want := []string{"after crds", "report crds", "after bedrock", "report bedrock"}
	if !slices.Equal(events, want) {
		t.Fatalf("events %v, want %v", events, want)
	}

	var failed []string
	report = func(group Group, err error) { failed = append(failed, fmt.Sprintf("%s %v", group.Name, err)) }
	stop := func(_ context.Context, group Group) error {
		if group.Name == "crds" {
			return fmt.Errorf("ovs-ovn on node-a: timeout")
		}
		return nil
	}
	err = InstallGroups(ctx, c, bundle, nil, Gates{}, 200*time.Millisecond, time.Minute, report, stop)
	if err == nil || err.Error() != "group crds: ovs-ovn on node-a: timeout" || !slices.Equal(failed, []string{"crds ovs-ovn on node-a: timeout"}) {
		t.Fatalf("err %v reports %v", err, failed)
	}
}

func TestReadInventoryWhenMissing(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	inv, err := ReadInventory(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 0 {
		t.Fatalf("expected empty inventory, got %d", len(inv))
	}
}

func TestWaitGroupWithCRDAndConfigMaps(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	bundle, err := Load(os.DirFS("testdata/good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	applier := Applier{Client: c}
	for _, group := range bundle.Groups {
		if err := applier.Apply(ctx, group); err != nil {
			t.Fatal(err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := WaitGroup(waitCtx, c, Gates{}, group, 200*time.Millisecond)
		cancel()
		if err != nil {
			t.Fatalf("group %s: %v", group.Name, err)
		}
	}
	blocked := Gates{schema.GroupKind{Group: "", Kind: "ConfigMap"}: func(*unstructured.Unstructured) Readiness { return Readiness{Message: "never"} }}
	waitCtx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	if err := WaitGroup(waitCtx, c, blocked, bundle.Groups[1], 100*time.Millisecond); err == nil {
		t.Fatal("expected timeout error")
	}

	failing := Gates{schema.GroupKind{Group: "", Kind: "ConfigMap"}: func(*unstructured.Unstructured) Readiness { return Readiness{Failed: true, Message: "boom"} }}
	failCtx, failCancel := context.WithTimeout(ctx, 5*time.Second)
	defer failCancel()
	start := time.Now()
	if err := WaitGroup(failCtx, c, failing, bundle.Groups[1], 100*time.Millisecond); err == nil {
		t.Fatal("expected failure error")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("expected fast failure, took %s", elapsed)
	}
}

func TestWaitGroupNotFoundThenTimeout(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "release-test"}}); err != nil {
		t.Fatal(err)
	}
	missing := &unstructured.Unstructured{}
	missing.SetAPIVersion("apps/v1")
	missing.SetKind("Deployment")
	missing.SetNamespace("release-test")
	missing.SetName("missing")
	group := Group{Name: "missing", Objects: []*unstructured.Unstructured{missing}}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := WaitGroup(waitCtx, c, Gates{}, group, 400*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "not found yet") {
		t.Fatalf("expected not found yet message, got %v", err)
	}
}
