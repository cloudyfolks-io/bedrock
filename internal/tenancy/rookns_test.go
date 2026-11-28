package tenancy

import (
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func TestProjectRolesCannotReachTheRookNamespace(t *testing.T) {
	c := projectRoleEnv(t, "rook-ceph")
	const rook = "ceph.rook.io"
	targets := []authorizationv1.ResourceAttributes{
		{Group: "", Resource: "secrets", Namespace: "rook-ceph"},
		{Group: rook, Resource: "cephobjectstoreaccounts", Namespace: "rook-ceph"},
		{Group: rook, Resource: "cephobjectstoreusers", Namespace: "rook-ceph"},
	}
	for _, user := range []string{"bedrock:vera", "bedrock:eddie", "bedrock:olga"} {
		for _, target := range targets {
			for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
				attributes := target
				attributes.Verb = verb
				if allowed(t, c, user, attributes) {
					t.Errorf("%s may %s %s/%s in the Rook namespace", user, verb, target.Group, target.Resource)
				}
			}
		}
	}
}

func TestProjectRolesStillCoverBothObjectStorageKinds(t *testing.T) {
	c := projectRoleEnv(t)
	const bedrock = "bedrock.cloudyfolks.io"
	for _, resource := range []string{"objectstorageusers", "objectstoragebuckets"} {
		read := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: "get", Namespace: "acme-shop"}
		write := authorizationv1.ResourceAttributes{Group: bedrock, Resource: resource, Verb: "create", Namespace: "acme-shop"}
		for user, want := range map[string][2]bool{"bedrock:vera": {true, false}, "bedrock:eddie": {true, true}, "bedrock:olga": {true, true}} {
			if allowed(t, c, user, read) != want[0] || allowed(t, c, user, write) != want[1] {
				t.Errorf("%s on %s: want read %v write %v", user, resource, want[0], want[1])
			}
		}
	}
}
