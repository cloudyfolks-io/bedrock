package operator

import "testing"

func TestOperatorRoleCoversObjectStorage(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	allowed := [][3]string{
		{"ceph.rook.io", "cephobjectstoreusers", "create"},
		{"ceph.rook.io", "cephobjectstoreusers", "delete"},
		{"ceph.rook.io", "cephobjectstoreusers", "patch"},
		{"ceph.rook.io", "cephobjectstoreusers", "get"},
		{"ceph.rook.io", "cephobjectstores", "get"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "create"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "delete"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "patch"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "get"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "list"},
		{"ceph.rook.io", "cephobjectstoreaccounts", "watch"},
		{"ceph.rook.io", "cephobjectstoreusers", "list"},
		{"ceph.rook.io", "cephobjectstoreusers", "watch"},
		{"cert-manager.io", "certificates", "create"},
		{"cert-manager.io", "certificates", "delete"},
		{"cert-manager.io", "certificates", "patch"},
		{"traefik.io", "ingressroutes", "create"},
		{"traefik.io", "ingressroutes", "delete"},
		{"traefik.io", "ingressroutes", "patch"},
		{"", "secrets", "create"},
		{"", "secrets", "delete"},
		{"", "secrets", "patch"},
		{"", "configmaps", "create"},
		{"", "configmaps", "patch"},
		{"", "configmaps", "watch"},
		{"objectbucket.io", "objectbucketclaims", "get"},
		{"objectbucket.io", "objectbucketclaims", "list"},
		{"objectbucket.io", "objectbucketclaims", "watch"},
		{"", "configmaps", "get"},
		{"", "secrets", "get"},
		{"", "namespaces", "get"},
		{"", "namespaces", "list"},
		{"", "namespaces", "watch"},
		{"bedrock.cloudyfolks.io", "objectstorageusers", "update"},
		{"bedrock.cloudyfolks.io", "objectstorageusers/status", "update"},
		{"bedrock.cloudyfolks.io", "objectstoragebuckets", "patch"},
		{"bedrock.cloudyfolks.io", "objectstoragebuckets/status", "update"},
	}
	for _, rule := range allowed {
		if !allows(role, rule[0], rule[1], rule[2]) {
			t.Errorf("missing %v", rule)
		}
	}
	if allows(role, "objectbucket.io", "objectbuckets", "delete") {
		t.Error("objectbuckets delete is allowed")
	}
	for _, verb := range []string{"create", "update", "patch", "delete"} {
		if allows(role, "objectbucket.io", "objectbucketclaims", verb) {
			t.Errorf("objectbucketclaims %s is allowed, but tenant buckets no longer use claims", verb)
		}
	}
}
