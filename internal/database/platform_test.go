package database

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func quota(hardCPU, usedCPU, hardMemory, usedMemory string) corev1.ResourceQuota {
	return corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
		Hard: corev1.ResourceList{"requests.cpu": resource.MustParse(hardCPU), "requests.memory": resource.MustParse(hardMemory)},
		Used: corev1.ResourceList{"requests.cpu": resource.MustParse(usedCPU), "requests.memory": resource.MustParse(usedMemory)},
	}}
}

func TestCheckCapacity(t *testing.T) {
	cpu, memory := resource.MustParse("2"), resource.MustParse("4Gi")
	cases := []struct {
		name   string
		quota  corev1.ResourceQuota
		reason string
	}{
		{"free", quota("32", "4", "128Gi", "8Gi"), ""},
		{"exactly at the limit", quota("10", "7", "100Gi", "86Gi"), ""},
		{"cpu one milli over", quota("10", "7001m", "100Gi", "10Gi"), ReasonCapacityExceeded},
		{"memory over", quota("100", "1", "10Gi", "6Gi"), ReasonCapacityExceeded},
		{"both over", quota("4", "4", "8Gi", "8Gi"), ReasonCapacityExceeded},
		{"no status", corev1.ResourceQuota{}, ReasonCapacityUnknown},
		{"no memory status", corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("32")},
			Used: corev1.ResourceList{"requests.cpu": resource.MustParse("1")},
		}}, ReasonCapacityUnknown},
	}
	for _, c := range cases {
		refusal := CheckCapacity(c.quota, cpu, memory, 0.9)
		switch {
		case c.reason == "" && refusal != nil:
			t.Errorf("%s: refused %+v", c.name, refusal)
		case c.reason != "" && (refusal == nil || refusal.Reason != c.reason || refusal.Message == ""):
			t.Errorf("%s: got %+v, want %s", c.name, refusal, c.reason)
		}
	}
}

func TestEnginesPolicyGolden(t *testing.T) {
	goldenObject(t, "engines_policy", EnginesPolicyObject())
}

func TestQuotaObjectFollowsTheSettings(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"database.capacity.cpu": "8", "database.capacity.memory": "16Gi", "database.capacity.pods": "20"})
	if err != nil {
		t.Fatal(err)
	}
	hard := QuotaObject(cfg).Spec.Hard
	cpu, memory, pods := hard["requests.cpu"], hard["requests.memory"], hard["pods"]
	if cpu.String() != "8" || memory.String() != "16Gi" || pods.String() != "20" || len(hard) != 3 {
		t.Fatalf("hard %v", hard)
	}
}

func TestEnsurePlatformMakesTheNamespaceQuotaAndPolicy(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	cfg, err := ParseConfig(map[string]string{"database.capacity.pods": "33"})
	must(t, err)
	must(t, EnsurePlatform(ctx, c, cfg))
	must(t, EnsurePlatform(ctx, c, cfg))

	var namespace corev1.Namespace
	must(t, c.Get(ctx, client.ObjectKey{Name: PlatformNamespace}, &namespace))
	var stored corev1.ResourceQuota
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &stored))
	pods := stored.Spec.Hard["pods"]
	if pods.String() != "33" {
		t.Fatalf("quota %v", stored.Spec.Hard)
	}
	var policy networkingv1.NetworkPolicy
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: EnginesPolicy}, &policy))

	cfg.CapacityPods = 44
	must(t, EnsurePlatform(ctx, c, cfg))
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &stored))
	pods = stored.Spec.Hard["pods"]
	if pods.String() != "44" {
		t.Fatalf("quota after a change %v", stored.Spec.Hard)
	}
}

func TestPlatformReconcilerMakesNothingWhileDisabledAndEverythingWhenEnabled(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	r := &PlatformReconciler{Client: c}
	_, err := r.Reconcile(ctx, reconcileRequest())
	must(t, err)
	var quotas corev1.ResourceQuotaList
	must(t, c.List(ctx, &quotas, client.InNamespace(PlatformNamespace), client.MatchingLabels{"bedrock.cloudyfolks.io/name": QuotaName}))
	before := len(quotas.Items)

	setSetting(t, c, "database.enabled", "true")
	setSetting(t, c, "database.capacity.pods", "77")
	_, err = r.Reconcile(ctx, reconcileRequest())
	must(t, err)
	var stored corev1.ResourceQuota
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &stored))
	pods := stored.Spec.Hard["pods"]
	if pods.String() != "77" || before > 1 {
		t.Fatalf("quota %v (before %d)", stored.Spec.Hard, before)
	}
}
