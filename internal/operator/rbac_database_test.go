package operator

import "testing"

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
			if !allows(role, resource[0], resource[1], verb) {
				t.Errorf("missing %s on %s/%s", verb, resource[0], resource[1])
			}
		}
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
