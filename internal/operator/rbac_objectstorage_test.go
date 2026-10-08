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
		{"objectbucket.io", "objectbucketclaims", "create"},
		{"objectbucket.io", "objectbucketclaims", "delete"},
		{"objectbucket.io", "objectbucketclaims", "patch"},
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
}
