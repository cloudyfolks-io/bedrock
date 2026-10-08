package tenancy

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestProjectsOfTenant(t *testing.T) {
	projects := []v1alpha1.Project{*projectFor("a", "acme"), *projectFor("b", "other"), *projectFor("c", "acme")}
	got := projectsOfTenant("acme", projects)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestNetworksOfTenant(t *testing.T) {
	networks := []v1alpha1.Network{*networkFor("a", "other"), *networkFor("b", "acme"), *networkFor("c", "acme")}
	got := networksOfTenant("acme", networks)
	if len(got) != 2 || got[0].Name != "b" || got[1].Name != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestTenantMappers(t *testing.T) {
	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "acme"}}}
	if got := tenantOfProject(context.Background(), projectFor("web", "acme")); !reflect.DeepEqual(got, want) {
		t.Fatalf("project: %v", got)
	}
	if got := tenantOfNetwork(context.Background(), networkFor("net", "acme")); !reflect.DeepEqual(got, want) {
		t.Fatalf("network: %v", got)
	}
	if got := tenantOfProject(context.Background(), &v1alpha1.Tenant{}); got != nil {
		t.Fatalf("other kind: %v", got)
	}
}

func elasticIPIn(name, namespace string) *v1alpha1.ElasticIP {
	return &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: "external"}}
}

func TestCountElasticIPs(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, namespaceNamed("acme-web", nil), namespaceNamed("other-web", nil))
	mustCreate(t, c, elasticIPIn("one", "acme-web"), elasticIPIn("two", "acme-web"), elasticIPIn("three", "other-web"))
	cases := []struct {
		namespaces []string
		want       int
	}{
		{[]string{"acme-web"}, 2},
		{[]string{"acme-web", "other-web"}, 3},
		{nil, 0},
	}
	for _, c2 := range cases {
		got, err := countElasticIPs(context.Background(), c, c2.namespaces)
		if err != nil || got != c2.want {
			t.Fatalf("namespaces %v: got %d, %v, want %d", c2.namespaces, got, err, c2.want)
		}
	}
}
