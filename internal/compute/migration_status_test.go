package compute

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func migrationObject(namespace, name, vmi, phase, failure string, created time.Time) unstructured.Unstructured {
	obj := emptyOf(kubeVirtMigrationGVK)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetCreationTimestamp(metav1.NewTime(created))
	obj.Object["spec"] = map[string]any{"vmiName": vmi}
	status := map[string]any{"phase": phase}
	if failure != "" {
		status["migrationState"] = map[string]any{"failureReason": failure}
	}
	obj.Object["status"] = status
	return *obj
}

func TestLatestMigration(t *testing.T) {
	vm := v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "web-1"}}
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		items []unstructured.Unstructured
		want  *v1alpha1.VMMigration
	}{
		"none": {},
		"newest of the VM wins": {
			items: []unstructured.Unstructured{
				migrationObject("acme-web", "old", "web-1", "Succeeded", "", base),
				migrationObject("acme-web", "new", "web-1", "Running", "", base.Add(time.Minute)),
			},
			want: &v1alpha1.VMMigration{Name: "new", Phase: "Running", StartedAt: &metav1.Time{Time: base.Add(time.Minute)}},
		},
		"another VM is ignored": {
			items: []unstructured.Unstructured{
				migrationObject("acme-web", "mine", "web-1", "Succeeded", "", base),
				migrationObject("acme-web", "theirs", "web-2", "Running", "", base.Add(time.Hour)),
			},
			want: &v1alpha1.VMMigration{Name: "mine", Phase: "Succeeded", StartedAt: &metav1.Time{Time: base}},
		},
		"another namespace is ignored": {
			items: []unstructured.Unstructured{
				migrationObject("acme-web", "mine", "web-1", "Succeeded", "", base),
				migrationObject("acme-db", "theirs", "web-1", "Running", "", base.Add(time.Hour)),
			},
			want: &v1alpha1.VMMigration{Name: "mine", Phase: "Succeeded", StartedAt: &metav1.Time{Time: base}},
		},
		"only a foreign one gives nothing": {
			items: []unstructured.Unstructured{migrationObject("acme-db", "theirs", "web-1", "Running", "", base)},
		},
		"failure carries the reason": {
			items: []unstructured.Unstructured{migrationObject("acme-web", "bad", "web-1", "Failed", "no space on the target", base)},
			want:  &v1alpha1.VMMigration{Name: "bad", Phase: "Failed", StartedAt: &metav1.Time{Time: base}, Message: "no space on the target"},
		},
		"empty phase is pending": {
			items: []unstructured.Unstructured{migrationObject("acme-web", "fresh", "web-1", "", "", base)},
			want:  &v1alpha1.VMMigration{Name: "fresh", Phase: "Pending", StartedAt: &metav1.Time{Time: base}},
		},
		"same time picks the later name": {
			items: []unstructured.Unstructured{
				migrationObject("acme-web", "a", "web-1", "Succeeded", "", base),
				migrationObject("acme-web", "b", "web-1", "Failed", "", base),
			},
			want: &v1alpha1.VMMigration{Name: "b", Phase: "Failed", StartedAt: &metav1.Time{Time: base}},
		},
	}
	for label, tc := range cases {
		got := LatestMigration(vm, tc.items)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: got %+v, want nothing", label, got)
		case tc.want != nil && (got == nil || got.Name != tc.want.Name || got.Phase != tc.want.Phase || got.Message != tc.want.Message || !got.StartedAt.Equal(tc.want.StartedAt)):
			t.Errorf("%s: got %+v, want %+v", label, got, tc.want)
		}
	}
}

func TestMigrationVMNameMapsTheVMI(t *testing.T) {
	migration := migrationObject("acme-web", "m", "web-1", "Running", "", time.Now())
	requests := migrationOwner(nil, &migration)
	if len(requests) != 1 || requests[0].Namespace != "acme-web" || requests[0].Name != "web-1" {
		t.Fatalf("%+v", requests)
	}
	empty := emptyOf(kubeVirtMigrationGVK)
	if migrationOwner(nil, empty) != nil {
		t.Fatal("a migration without vmiName maps to nothing")
	}
}
