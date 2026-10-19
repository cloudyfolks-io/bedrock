package tenancy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var epoch = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func quantity(text string) *resource.Quantity {
	parsed := resource.MustParse(text)
	return &parsed
}

func projectAt(name string, offset time.Duration, quota v1alpha1.ProjectQuota, namespace string) v1alpha1.Project {
	return v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(epoch.Add(offset))},
		Spec:       v1alpha1.ProjectSpec{TenantRef: "acme", Quota: quota},
		Status:     v1alpha1.ProjectStatus{Namespace: namespace},
	}
}

func cpuQuota(cpu string) v1alpha1.ProjectQuota {
	return v1alpha1.ProjectQuota{CPU: quantity(cpu)}
}

func shuffles(projects []v1alpha1.Project) [][]v1alpha1.Project {
	n := len(projects)
	out := make([][]v1alpha1.Project, 0, n)
	for shift := 0; shift < n; shift++ {
		rotated := append(append([]v1alpha1.Project{}, projects[shift:]...), projects[:shift]...)
		out = append(out, rotated)
	}
	return out
}

func TestDecideAdmitsInCreationOrder(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Projects: ptr.To(int32(2))}
	projects := []v1alpha1.Project{
		projectAt("first", 0, v1alpha1.ProjectQuota{}, ""),
		projectAt("second", time.Second, v1alpha1.ProjectQuota{}, ""),
		projectAt("third", 2*time.Second, v1alpha1.ProjectQuota{}, ""),
	}
	for _, order := range shuffles(projects) {
		if got := Decide(ceiling, order, "first"); !got.Admitted {
			t.Fatalf("first: %+v", got)
		}
		if got := Decide(ceiling, order, "second"); !got.Admitted {
			t.Fatalf("second: %+v", got)
		}
		got := Decide(ceiling, order, "third")
		if got.Admitted || got.QuotaAllowed || got.Reason != ReasonProjectQuotaExceeded {
			t.Fatalf("third: %+v", got)
		}
	}
}

func TestDecideTieBreaksByName(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Projects: ptr.To(int32(1))}
	projects := []v1alpha1.Project{projectAt("b", 0, v1alpha1.ProjectQuota{}, ""), projectAt("a", 0, v1alpha1.ProjectQuota{}, "")}
	if got := Decide(ceiling, projects, "a"); !got.Admitted {
		t.Fatalf("a: %+v", got)
	}
	if got := Decide(ceiling, projects, "b"); got.Admitted {
		t.Fatalf("b: %+v", got)
	}
}

func TestDecideProjectCountCeiling(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Projects: ptr.To(int32(0))}
	got := Decide(ceiling, []v1alpha1.Project{projectAt("a", 0, v1alpha1.ProjectQuota{}, "")}, "a")
	if got.Admitted || got.Message != "tenant acme allows 0 projects" {
		t.Fatalf("got %+v", got)
	}
}

func TestDecideAllocationCeiling(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{CPU: quantity("8")}
	projects := []v1alpha1.Project{projectAt("older", 0, cpuQuota("5"), ""), projectAt("newer", time.Second, cpuQuota("4"), "")}
	got := Decide(ceiling, projects, "newer")
	if got.Admitted || got.QuotaAllowed || got.Reason != ReasonTenantQuotaExceeded || !strings.Contains(got.Message, "cpu") {
		t.Fatalf("got %+v", got)
	}
	if older := Decide(ceiling, projects, "older"); !older.Admitted || !older.QuotaAllowed {
		t.Fatalf("older: %+v", older)
	}
}

func TestDecideChecksEveryAllocatedResource(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Memory: quantity("16Gi"), Storage: quantity("200Gi")}
	cases := []struct {
		name     string
		first    v1alpha1.ProjectQuota
		second   v1alpha1.ProjectQuota
		resource string
	}{
		{"memory", v1alpha1.ProjectQuota{Memory: quantity("10Gi"), Storage: quantity("150Gi")}, v1alpha1.ProjectQuota{Memory: quantity("10Gi"), Storage: quantity("10Gi")}, "memory"},
		{"storage", v1alpha1.ProjectQuota{Memory: quantity("4Gi"), Storage: quantity("150Gi")}, v1alpha1.ProjectQuota{Memory: quantity("4Gi"), Storage: quantity("60Gi")}, "storage"},
	}
	for _, c := range cases {
		projects := []v1alpha1.Project{projectAt("a", 0, c.first, ""), projectAt("b", time.Second, c.second, "")}
		got := Decide(ceiling, projects, "b")
		if got.Admitted || got.Reason != ReasonTenantQuotaExceeded || !strings.Contains(got.Message, c.resource) {
			t.Fatalf("%s: %+v", c.name, got)
		}
	}
}

func TestDecideKeepsProvisionedProjects(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Projects: ptr.To(int32(1))}
	projects := []v1alpha1.Project{
		projectAt("newer", time.Second, v1alpha1.ProjectQuota{}, "acme-newer"),
		projectAt("older", 0, v1alpha1.ProjectQuota{}, "acme-older"),
		projectAt("fresh", 2*time.Second, v1alpha1.ProjectQuota{}, ""),
	}
	for _, name := range []string{"newer", "older"} {
		if got := Decide(ceiling, projects, name); !got.Admitted {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if got := Decide(ceiling, projects, "fresh"); got.Admitted {
		t.Fatalf("fresh: %+v", got)
	}
}

func TestDecideProvisionedOverAllocationLosesOnlyTheQuota(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{CPU: quantity("8")}
	projects := []v1alpha1.Project{projectAt("a-data", 0, cpuQuota("4"), "acme-a-data"), projectAt("b-web", time.Second, cpuQuota("6"), "acme-b-web")}
	got := Decide(ceiling, projects, "b-web")
	if !got.Admitted || got.QuotaAllowed || got.Reason != ReasonTenantQuotaExceeded {
		t.Fatalf("got %+v", got)
	}
	if first := Decide(ceiling, projects, "a-data"); !first.Admitted || !first.QuotaAllowed {
		t.Fatalf("a-data: %+v", first)
	}
}

func TestDecideUnsetCeilingIsUnlimited(t *testing.T) {
	projects := []v1alpha1.Project{projectAt("a", 0, cpuQuota("1000"), ""), projectAt("b", time.Second, cpuQuota("1000"), "")}
	for _, name := range []string{"a", "b"} {
		if got := Decide(v1alpha1.TenantQuota{}, projects, name); !got.Admitted || !got.QuotaAllowed {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

func TestDecideUnknownProjectIsTheZeroDecision(t *testing.T) {
	if got := Decide(v1alpha1.TenantQuota{}, nil, "ghost"); got != (Decision{}) {
		t.Fatalf("got %+v", got)
	}
}

func TestQuotaHardMapsEveryResource(t *testing.T) {
	got := QuotaHard(v1alpha1.ProjectQuota{CPU: quantity("8"), Memory: quantity("16Gi"), Storage: quantity("200Gi"), VMs: ptr.To(int32(5)), Containers: ptr.To(int32(12)), KubernetesClusters: ptr.To(int32(2))})
	want := map[corev1.ResourceName]string{
		"requests.cpu":     "8",
		"requests.memory":  "16Gi",
		"requests.storage": "200Gi",
		"count/virtualmachines.bedrock.cloudyfolks.io":    "5",
		"count/containers.bedrock.cloudyfolks.io":         "12",
		"count/kubernetesclusters.bedrock.cloudyfolks.io": "2",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for name, text := range want {
		value := got[name]
		if value.String() != text {
			t.Fatalf("%s = %s, want %s", name, value.String(), text)
		}
	}
}

func TestQuotaHardOmitsUnsetResources(t *testing.T) {
	if got := QuotaHard(cpuQuota("2")); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if got := QuotaHard(v1alpha1.ProjectQuota{}); got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestProjectUsageMapsBack(t *testing.T) {
	used := corev1.ResourceList{
		"requests.cpu":     resource.MustParse("2"),
		"requests.memory":  resource.MustParse("4Gi"),
		"requests.storage": resource.MustParse("10Gi"),
		"count/virtualmachines.bedrock.cloudyfolks.io":    resource.MustParse("3"),
		"count/containers.bedrock.cloudyfolks.io":         resource.MustParse("4"),
		"count/kubernetesclusters.bedrock.cloudyfolks.io": resource.MustParse("1"),
		"pods": resource.MustParse("9"),
	}
	got := ProjectUsage(used)
	if got.CPU.String() != "2" || got.Memory.String() != "4Gi" || got.Storage.String() != "10Gi" || *got.VMs != 3 || *got.Containers != 4 || *got.KubernetesClusters != 1 {
		t.Fatalf("got %+v", got)
	}
	partial := ProjectUsage(corev1.ResourceList{"requests.cpu": resource.MustParse("1")})
	if partial.Memory != nil || partial.Storage != nil || partial.VMs != nil || partial.Containers != nil || partial.KubernetesClusters != nil {
		t.Fatalf("partial %+v", partial)
	}
}

func TestTenantUsageSumsProjects(t *testing.T) {
	first := projectAt("a", 0, v1alpha1.ProjectQuota{}, "")
	first.Status.Usage = v1alpha1.ProjectQuota{CPU: quantity("2"), Memory: quantity("4Gi")}
	second := projectAt("b", 0, v1alpha1.ProjectQuota{}, "")
	second.Status.Usage = v1alpha1.ProjectQuota{CPU: quantity("3"), Storage: quantity("10Gi")}
	got := TenantUsage([]v1alpha1.Project{first, second}, 2)
	if fmt.Sprintf("%s %s %s %d %d", got.CPU, got.Memory, got.Storage, *got.Projects, *got.ElasticIPs) != "5 4Gi 10Gi 2 2" {
		t.Fatalf("got %+v", got)
	}
}

func TestTenantUsageWithoutProjects(t *testing.T) {
	got := TenantUsage(nil, 0)
	if !got.CPU.IsZero() || !got.Memory.IsZero() || !got.Storage.IsZero() || *got.Projects != 0 || *got.ElasticIPs != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDecideRequiresEveryCeilingedResource(t *testing.T) {
	full := v1alpha1.ProjectQuota{CPU: quantity("1"), Memory: quantity("1Gi"), Storage: quantity("1Gi")}
	cases := []struct {
		name     string
		ceiling  v1alpha1.TenantQuota
		without  func(v1alpha1.ProjectQuota) v1alpha1.ProjectQuota
		resource string
	}{
		{"cpu", v1alpha1.TenantQuota{CPU: quantity("8")}, func(q v1alpha1.ProjectQuota) v1alpha1.ProjectQuota { q.CPU = nil; return q }, "cpu"},
		{"memory", v1alpha1.TenantQuota{Memory: quantity("16Gi")}, func(q v1alpha1.ProjectQuota) v1alpha1.ProjectQuota { q.Memory = nil; return q }, "memory"},
		{"storage", v1alpha1.TenantQuota{Storage: quantity("200Gi")}, func(q v1alpha1.ProjectQuota) v1alpha1.ProjectQuota { q.Storage = nil; return q }, "storage"},
	}
	for _, c := range cases {
		missing := []v1alpha1.Project{projectAt("a", 0, c.without(full), "")}
		got := Decide(c.ceiling, missing, "a")
		if got.Admitted || got.QuotaAllowed || got.Reason != ReasonQuotaRequired || !strings.Contains(got.Message, c.resource) {
			t.Fatalf("%s unprovisioned: %+v", c.name, got)
		}
		kept := []v1alpha1.Project{projectAt("a", 0, c.without(full), "acme-a")}
		got = Decide(c.ceiling, kept, "a")
		if !got.Admitted || got.QuotaAllowed || got.Reason != ReasonQuotaRequired {
			t.Fatalf("%s provisioned: %+v", c.name, got)
		}
		complete := []v1alpha1.Project{projectAt("a", 0, full, "")}
		if got := Decide(c.ceiling, complete, "a"); !got.Admitted || !got.QuotaAllowed {
			t.Fatalf("%s complete: %+v", c.name, got)
		}
	}
}

func TestDecideAcceptsUnsetResourcesWithoutCeiling(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{CPU: quantity("8")}
	projects := []v1alpha1.Project{projectAt("a", 0, cpuQuota("1"), "")}
	if got := Decide(ceiling, projects, "a"); !got.Admitted || !got.QuotaAllowed {
		t.Fatalf("got %+v", got)
	}
}

func TestDecideConflictedProjectHoldsNoSlot(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{Projects: ptr.To(int32(1))}
	stuck := projectAt("a-stuck", 0, v1alpha1.ProjectQuota{}, "")
	stuck.Status.Conditions = []metav1.Condition{{Type: ConditionNamespaceReady, Status: metav1.ConditionFalse, Reason: ReasonNamespaceConflict}}
	projects := []v1alpha1.Project{stuck, projectAt("b-free", time.Second, v1alpha1.ProjectQuota{}, "")}
	if got := Decide(ceiling, projects, "b-free"); !got.Admitted {
		t.Fatalf("b-free: %+v", got)
	}
}

func TestDecideDeniedProvisionedProjectDoesNotPoisonLaterOnes(t *testing.T) {
	ceiling := v1alpha1.TenantQuota{CPU: quantity("8")}
	projects := []v1alpha1.Project{
		projectAt("a", 0, cpuQuota("4"), "acme-a"),
		projectAt("b", time.Second, cpuQuota("6"), "acme-b"),
		projectAt("c", 2*time.Second, cpuQuota("3"), "acme-c"),
	}
	if got := Decide(ceiling, projects, "b"); got.QuotaAllowed {
		t.Fatalf("b: %+v", got)
	}
	if got := Decide(ceiling, projects, "c"); !got.QuotaAllowed {
		t.Fatalf("c: %+v", got)
	}
}
