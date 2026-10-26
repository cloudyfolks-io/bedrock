package operator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func hostWithLegCondition(name string, status metav1.ConditionStatus, reason, message string) *v1alpha1.Host {
	return &v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1alpha1.HostStatus{Conditions: []metav1.Condition{{
			Type: v1alpha1.ConditionTenantLegsPresent, Status: status, Reason: reason, Message: message, LastTransitionTime: metav1.Now(),
		}}},
	}
}

func TestTenantLegsGuardedIsFalseWhenAHostLacksALegInterface(t *testing.T) {
	r, c := nodePortReconciler(t, "",
		hostWithLegCondition("host-01", metav1.ConditionTrue, "Present", ""),
		hostWithLegCondition("host-02", metav1.ConditionFalse, "LegInterfaceMissing", "br-public.30"),
	)
	r.Recorder = nil
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := legsGuardedCondition(t, c)
	if got == nil || got.Status != metav1.ConditionFalse || got.Reason != "LegInterfaceMissing" {
		t.Fatalf("condition %+v", got)
	}
	if !strings.Contains(got.Message, "host-02") || !strings.Contains(got.Message, "br-public.30") || strings.Contains(got.Message, "host-01") {
		t.Fatalf("message %q", got.Message)
	}
}

func TestTenantLegsGuardedIsTrueWhenEveryHostHasTheLegs(t *testing.T) {
	r, c := nodePortReconciler(t, "", hostWithLegCondition("host-01", metav1.ConditionTrue, "Present", ""))
	setTenantLegs(t, c, []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}})
	if err := r.reconcileTenantLegs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := legsGuardedCondition(t, c); got == nil || got.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", got)
	}
}
