package operator

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database"
)

func TestOperatorRoleCoversDatabases(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	standard := []string{"create", "get", "list", "patch", "update", "watch", "delete"}
	resources := [][2]string{
		{"postgresql.cnpg.io", "clusters"},
		{"postgresql.cnpg.io", "scheduledbackups"},
		{"postgresql.cnpg.io", "backups"},
		{"barmancloud.cnpg.io", "objectstores"},
		{"", "serviceaccounts"},
		{"", "secrets"},
		{"", "services"},
		{"", "persistentvolumeclaims"},
		{"", "configmaps"},
		{"", "resourcequotas"},
		{"networking.k8s.io", "networkpolicies"},
		{"apps", "deployments"},
		{"cert-manager.io", "certificates"},
		{"bedrock.cloudyfolks.io", "securitygroups"},
		{"bedrock.cloudyfolks.io", "databases"},
		{"bedrock.cloudyfolks.io", "databaseusers"},
		{"bedrock.cloudyfolks.io", "databaseschemas"},
		{"bedrock.cloudyfolks.io", "databaseversions"},
		{"bedrock.cloudyfolks.io", "databaseclasses"},
	}
	for _, resource := range resources {
		for _, verb := range standard {
			if resource[1] == "secrets" && verb == "watch" {
				continue
			}
			if !allows(role, resource[0], resource[1], verb) {
				t.Errorf("missing %s on %s/%s", verb, resource[0], resource[1])
			}
		}
	}
	if allows(role, "", "secrets", "watch") {
		t.Error("the role must not watch Secrets")
	}
	for _, resource := range []string{"databases/status", "databaseusers/status", "databaseschemas/status"} {
		if !allows(role, "bedrock.cloudyfolks.io", resource, "update") {
			t.Errorf("missing update on %s", resource)
		}
	}
	for _, verb := range []string{"get", "list", "watch"} {
		if !allows(role, "node.k8s.io", "runtimeclasses", verb) {
			t.Errorf("missing %s on runtimeclasses", verb)
		}
	}
	if !allows(role, "", "pods/exec", "create") || !allows(role, "", "pods", "list") {
		t.Error("the operator must run SQL in the engine pods")
	}
	for _, denied := range [][3]string{
		{"", "namespaces", "delete"},
		{"apiextensions.k8s.io", "customresourcedefinitions", "delete"},
		{"postgresql.cnpg.io", "poolers", "create"},
	} {
		if allows(role, denied[0], denied[1], denied[2]) {
			t.Errorf("%v is allowed", denied)
		}
	}
}

func TestOperatorRoleCannotWatchSecrets(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if allows(role, "", "secrets", "watch") {
		t.Error("the operator must not watch Secrets")
	}
	for _, verb := range []string{"get", "create", "patch", "update", "delete"} {
		if !allows(role, "", "secrets", verb) {
			t.Errorf("missing %s on secrets", verb)
		}
	}
}

func TestOperatorRoleCoversEveryTypeTheDatabaseControllersCache(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, object := range database.CachedTypes() {
		gvks, _, err := scheme.ObjectKinds(object)
		if err != nil {
			t.Fatal(err)
		}
		resource := resourceOf(gvks[0].GroupKind(), nil)
		if resource == "secrets" {
			t.Fatal("the database controllers must not cache Secrets")
		}
		for _, verb := range []string{"list", "watch"} {
			if !allows(role, gvks[0].Group, resource, verb) {
				t.Errorf("the controllers cache %s but the role has no %s on %s/%s", gvks[0].Kind, verb, gvks[0].Group, resource)
			}
		}
	}
}
