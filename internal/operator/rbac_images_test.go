package operator

import (
	"slices"
	"testing"
)

func TestOperatorRoleCoversImages(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	full := []string{"create", "get", "list", "patch", "update", "watch", "delete"}
	for _, target := range []struct{ group, resource string }{
		{"cdi.kubevirt.io", "datavolumes"},
		{"cdi.kubevirt.io", "datasources"},
		{"cdi.kubevirt.io", "dataimportcrons"},
		{"snapshot.storage.k8s.io", "volumesnapshots"},
		{"traefik.io", "ingressroutes"},
		{"traefik.io", "middlewares"},
		{"traefik.io", "serverstransports"},
		{"", "configmaps"},
	} {
		for _, verb := range full {
			if !allows(role, target.group, target.resource, verb) {
				t.Errorf("%s/%s must allow %s", target.group, target.resource, verb)
			}
		}
	}
	for _, resource := range []string{"persistentvolumeclaims", "namespaces"} {
		for _, verb := range []string{"get", "list", "watch"} {
			if !allows(role, "", resource, verb) {
				t.Errorf("%s must allow %s", resource, verb)
			}
		}
	}
	for _, rule := range imagesRules() {
		if slices.Contains(rule.Resources, "persistentvolumeclaims") && slices.Contains(rule.Verbs, "delete") {
			t.Error("claims are read only for the image controllers")
		}
	}
}

func TestOperatorRoleHasNoCloneSource(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range role.Rules {
		if !slices.Contains(rule.APIGroups, "cdi.kubevirt.io") && !slices.Contains(rule.APIGroups, "upload.cdi.kubevirt.io") {
			continue
		}
		if slices.Contains(rule.Resources, "datavolumes/source") || slices.Contains(rule.Resources, "*") || slices.Contains(rule.Resources, "uploadtokenrequests") {
			t.Fatalf("operator role must not hold %+v", rule)
		}
	}
}
