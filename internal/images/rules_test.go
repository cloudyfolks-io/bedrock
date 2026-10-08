package images

import (
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func TestUploadHandlerRules(t *testing.T) {
	want := []rbacv1.PolicyRule{
		{APIGroups: []string{"upload.cdi.kubevirt.io"}, Resources: []string{"uploadtokenrequests"}, Verbs: []string{"create"}},
		{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"images"}, Verbs: []string{"get"}},
		{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"settings"}, Verbs: []string{"get", "list"}},
	}
	if got := UploadHandlerRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules %+v", got)
	}
}
