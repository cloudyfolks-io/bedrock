package methods

import (
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

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
