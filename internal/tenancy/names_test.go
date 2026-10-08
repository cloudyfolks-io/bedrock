package tenancy

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestProjectObjectLabels(t *testing.T) {
	project := v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Spec: v1alpha1.ProjectSpec{TenantRef: "acme"}}
	want := v1alpha1.TenantLabels("Project", "shop", "acme", "shop")
	if got := ProjectObjectLabels(project); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestTenantObjectLabels(t *testing.T) {
	tenant := v1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	got := TenantObjectLabels(tenant)
	if want := v1alpha1.TenantLabels("Tenant", "acme", "acme", ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if _, found := got[v1alpha1.LabelProject]; found {
		t.Fatalf("tenant labels carry a project label: %v", got)
	}
}
