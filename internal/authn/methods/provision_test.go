package methods

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type createRaceClient struct {
	client.Client
	beforeCreate func()
}

func (c *createRaceClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if hook := c.beforeCreate; hook != nil {
		c.beforeCreate = nil
		hook()
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestProvisionUserCreatesOrUpdates(t *testing.T) {
	identity := ExternalIdentity{Username: "alice", DisplayName: "Alice A", Email: "alice@example.test", Groups: []string{"developers"}}
	created := ProvisionUser(nil, "corp-ldap", identity)
	if created.Name != v1alpha1.UserObjectName("alice") || created.Spec.Source != "corp-ldap" {
		t.Fatalf("created = %+v", created)
	}
	if created.Spec.DisplayName != "Alice A" || created.Spec.Email != "alice@example.test" {
		t.Fatalf("created spec = %+v", created.Spec)
	}

	existing := created
	existing.ResourceVersion = "42"
	updatedIdentity := ExternalIdentity{Username: "alice", DisplayName: "Alice Updated", Email: "alice@example.test", Groups: []string{"developers", "operators"}}
	updated := ProvisionUser(&existing, "corp-ldap", updatedIdentity)
	if updated.ResourceVersion != "42" {
		t.Fatal("updating an existing user must keep its resourceVersion")
	}
	if updated.Spec.DisplayName != "Alice Updated" || len(updated.Spec.Groups) != 2 {
		t.Fatalf("updated = %+v", updated)
	}

	oidcIdentity := ExternalIdentity{Username: "bob", Subject: "upstream-subject-1"}
	withSubject := ProvisionUser(nil, "corp-oidc", oidcIdentity)
	if withSubject.Status.UpstreamSubject != "upstream-subject-1" {
		t.Fatalf("withSubject = %+v", withSubject)
	}
}

func TestSourceAndSubjectMismatch(t *testing.T) {
	user := v1alpha1.User{Spec: v1alpha1.UserSpec{Source: "corp-oidc"}, Status: v1alpha1.UserStatus{UpstreamSubject: "sub-1"}}
	if sourceMismatch(user, "corp-oidc") {
		t.Fatal("the same source must not mismatch")
	}
	if !sourceMismatch(user, "corp-ldap") {
		t.Fatal("a different source must mismatch")
	}
	if subjectMismatch(user, "sub-1") {
		t.Fatal("the same subject must not mismatch")
	}
	if !subjectMismatch(user, "sub-2") {
		t.Fatal("a different subject must mismatch")
	}
	fresh := v1alpha1.User{}
	if subjectMismatch(fresh, "sub-1") {
		t.Fatal("an empty upstream subject must not mismatch a first bind")
	}
}

func TestCreateOrExistingUserReturnsFreshOnConflict(t *testing.T) {
	c, _ := startTestEnv(t)
	created := ProvisionUser(nil, "corp-oidc", ExternalIdentity{Username: "collide"})

	first, existed, err := CreateOrExistingUser(context.Background(), c, created)
	if err != nil {
		t.Fatal(err)
	}
	if existed {
		t.Fatalf("the first create must not report existed: %+v", first)
	}

	again, existed, err := CreateOrExistingUser(context.Background(), c, created)
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatalf("a colliding create must report existed: %+v", again)
	}
	if again.Name != first.Name {
		t.Fatalf("again = %+v, want the same object as first = %+v", again, first)
	}
}
