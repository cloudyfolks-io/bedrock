package tenancy

import (
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func TestSubjectCarriesTheAuthnPrefix(t *testing.T) {
	cases := []struct {
		kind, name, want string
	}{
		{"User", "alice", "bedrock:alice"},
		{"Group", "ops", "bedrock:ops"},
		{"Group", "system:masters", "bedrock:system:masters"},
		{"User", "admin", "bedrock:admin"},
	}
	for _, c := range cases {
		got, ok := Subject(c.kind, c.name)
		if !ok {
			t.Fatalf("Subject(%q, %q) refused", c.kind, c.name)
		}
		want := rbacv1.Subject{APIGroup: "rbac.authorization.k8s.io", Kind: c.kind, Name: c.want}
		if got != want {
			t.Fatalf("Subject(%q, %q) = %+v, want %+v", c.kind, c.name, got, want)
		}
	}
}

func TestSubjectRefusesOtherKinds(t *testing.T) {
	for _, c := range [][2]string{{"ServiceAccount", "x"}, {"", "x"}, {"User", ""}} {
		if _, ok := Subject(c[0], c[1]); ok {
			t.Fatalf("Subject(%q, %q) accepted", c[0], c[1])
		}
	}
}

func TestSortedSubjects(t *testing.T) {
	in := []rbacv1.Subject{
		{Kind: "Group", Name: "bedrock:ops"},
		{Kind: "User", Name: "bedrock:bob"},
		{Kind: "Group", Name: "bedrock:dev"},
		{Kind: "User", Name: "bedrock:alice"},
		{Kind: "User", Name: "bedrock:alice"},
	}
	before := append([]rbacv1.Subject(nil), in...)
	want := []rbacv1.Subject{
		{Kind: "Group", Name: "bedrock:dev"},
		{Kind: "Group", Name: "bedrock:ops"},
		{Kind: "User", Name: "bedrock:alice"},
		{Kind: "User", Name: "bedrock:bob"},
	}
	if got := SortedSubjects(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("input changed: %v", in)
	}
}
