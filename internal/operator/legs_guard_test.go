package operator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func legsGuardReconciler(t *testing.T) (*ClusterReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	r, c := nodePortReconciler(t, "")
	recorder := record.NewFakeRecorder(4)
	r.Recorder = recorder
	return r, c, recorder
}

func setTenantLegs(t *testing.T, c client.Client, legs []v1alpha1.TenantLegSpec) {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Spec.TenantLegs = legs
	if err := c.Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
}

func legsGuardedCondition(t *testing.T, c client.Client) *metav1.Condition {
	t.Helper()
	return clusterConditionOf(t, c, v1alpha1.ConditionTenantLegsGuarded)
}

func TestTenantLegsGuardedFollowsTheList(t *testing.T) {
	r, c, recorder := legsGuardReconciler(t)
	ctx := context.Background()
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got != nil {
		t.Fatalf("a cluster that never had legs has no condition: %+v", got)
	}
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got == nil || got.Status != metav1.ConditionTrue || got.Reason != "Guarded" {
		t.Fatalf("condition %+v", got)
	}
	setTenantLegs(t, c, nil)
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got == nil || got.Status != metav1.ConditionFalse || got.Reason != "NoTenantLegs" {
		t.Fatalf("condition %+v", got)
	}
	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, "Warning NoTenantLegs ") {
			t.Fatalf("event %q", event)
		}
	default:
		t.Fatal("removing the legs must raise a Warning event")
	}
}

func TestTenantLegsRemovalWarnsOnce(t *testing.T) {
	r, c, recorder := legsGuardReconciler(t)
	ctx := context.Background()
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	setTenantLegs(t, c, nil)
	for range 3 {
		if err := r.reconcileTenantLegs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("want one event, got %d", len(recorder.Events))
	}
}

func TestTenantLegsReturnGuardsAgain(t *testing.T) {
	r, c, _ := legsGuardReconciler(t)
	ctx := context.Background()
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	setTenantLegs(t, c, nil)
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got == nil || got.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", got)
	}
}

func managedHost(name string, applied metav1.ConditionStatus) *v1alpha1.Host {
	return &v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.HostSpec{Management: v1alpha1.ManagementSpec{Enabled: true}},
		Status: v1alpha1.HostStatus{Conditions: []metav1.Condition{
			{Type: v1alpha1.ConditionManagementApplied, Status: applied, Reason: "r"},
			{Type: v1alpha1.ConditionTenantLegsPresent, Status: metav1.ConditionTrue, Reason: "Present"},
		}},
	}
}

func TestTenantLegsGuardedNeedsEveryHostToApplyTheGuard(t *testing.T) {
	r, c, _ := legsGuardReconciler(t)
	ctx := context.Background()
	for _, host := range []*v1alpha1.Host{managedHost("node-a", metav1.ConditionTrue), managedHost("node-b", metav1.ConditionFalse)} {
		if err := c.Create(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	got := legsGuardedCondition(t, c)
	if got == nil || got.Status != metav1.ConditionFalse || got.Reason != "HostsNotGuarded" || !strings.Contains(got.Message, "node-b") || strings.Contains(got.Message, "node-a") {
		t.Fatalf("condition %+v", got)
	}
	var node v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "node-b"}, &node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions[0].Status = metav1.ConditionTrue
	if err := c.Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileTenantLegs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got == nil || got.Status != metav1.ConditionTrue || got.Reason != "Guarded" {
		t.Fatalf("condition %+v", got)
	}
}

func TestTenantLegsGuardedNeedsPresentLegsOnManagedHosts(t *testing.T) {
	cases := []struct {
		name       string
		conditions []metav1.Condition
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{"not reported yet", []metav1.Condition{{Type: v1alpha1.ConditionManagementApplied, Status: metav1.ConditionTrue, Reason: "r"}}, metav1.ConditionFalse, "HostsNotGuarded"},
		{"guard failed", []metav1.Condition{
			{Type: v1alpha1.ConditionManagementApplied, Status: metav1.ConditionTrue, Reason: "r"},
			{Type: v1alpha1.ConditionTenantLegsPresent, Status: metav1.ConditionFalse, Reason: "GuardFailed"},
		}, metav1.ConditionFalse, "HostsNotGuarded"},
		{"missing interface", []metav1.Condition{
			{Type: v1alpha1.ConditionManagementApplied, Status: metav1.ConditionTrue, Reason: "r"},
			{Type: v1alpha1.ConditionTenantLegsPresent, Status: metav1.ConditionFalse, Reason: "LegInterfaceMissing", Message: "br-public.30"},
		}, metav1.ConditionFalse, "LegInterfaceMissing"},
		{"all present", []metav1.Condition{
			{Type: v1alpha1.ConditionManagementApplied, Status: metav1.ConditionTrue, Reason: "r"},
			{Type: v1alpha1.ConditionTenantLegsPresent, Status: metav1.ConditionTrue, Reason: "Present"},
		}, metav1.ConditionTrue, "Guarded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, c, _ := legsGuardReconciler(t)
			host := managedHost("node-a", metav1.ConditionTrue)
			host.Status.Conditions = tc.conditions
			if err := c.Create(context.Background(), host); err != nil {
				t.Fatal(err)
			}
			setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
			if err := r.reconcileTenantLegs(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := legsGuardedCondition(t, c)
			if got == nil || got.Status != tc.wantStatus || got.Reason != tc.wantReason {
				t.Fatalf("condition %+v", got)
			}
			if tc.wantStatus == metav1.ConditionFalse && !strings.Contains(got.Message, "node-a") {
				t.Fatalf("message %q", got.Message)
			}
		})
	}
}
