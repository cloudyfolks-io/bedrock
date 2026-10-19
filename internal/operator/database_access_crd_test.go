package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func databaseUser(name string, grants ...v1alpha1.DatabaseGrant) *v1alpha1.DatabaseUser {
	return &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.DatabaseUserSpec{DatabaseRef: "orders", Grants: grants},
	}
}

func TestDatabaseUserValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	user := databaseUser("alice", v1alpha1.DatabaseGrant{Database: "app", Level: "readwrite"}, v1alpha1.DatabaseGrant{Database: "orders", Level: "readonly"})
	user.Spec.Username = "alice_b"
	createValid(t, c, user)
	user.Status.Username = "alice_b"
	user.Status.CredentialsSecret = "alice-db"
	user.Status.AppliedGrants = "0123456789abcdef"
	if err := c.Status().Update(ctx, user); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createValid(t, c, databaseUser("nogrants"))

	createInvalid(t, c, databaseUser("badlevel", v1alpha1.DatabaseGrant{Database: "app", Level: "owner"}), "spec.grants")
	createInvalid(t, c, databaseUser("baddb", v1alpha1.DatabaseGrant{Database: "App-1", Level: "all"}), "spec.grants")
	createInvalid(t, c, databaseUser("twice", v1alpha1.DatabaseGrant{Database: "app", Level: "all"}, v1alpha1.DatabaseGrant{Database: "app", Level: "readonly"}), "spec.grants")
	badName := databaseUser("badname")
	badName.Spec.Username = "Alice-B"
	createInvalid(t, c, badName, "spec.username")
	long := databaseUser("a1234567890123456789012345678901234567890123456789012")
	createInvalid(t, c, long, "at most 52 characters")
}

func TestDatabaseUserReferenceAndUsernameAreImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)

	user := databaseUser("alice")
	user.Spec.Username = "alice_b"
	createValid(t, c, user)

	moved := user.DeepCopy()
	moved.Spec.DatabaseRef = "other"
	updateInvalid(t, c, moved, "databaseRef is immutable")
	renamed := user.DeepCopy()
	renamed.Spec.Username = "alice_c"
	updateInvalid(t, c, renamed, "username is immutable")
	cleared := user.DeepCopy()
	cleared.Spec.Username = ""
	updateInvalid(t, c, cleared, "username is immutable")

	changed := user.DeepCopy()
	changed.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "app", Level: "all"}}
	if err := c.Update(context.Background(), changed); err != nil {
		t.Fatalf("grants stay mutable: %v", err)
	}
}

func databaseSchema(name string) *v1alpha1.DatabaseSchema {
	return &v1alpha1.DatabaseSchema{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.DatabaseSchemaSpec{DatabaseRef: "orders"},
	}
}

func TestDatabaseSchemaValidationAndDefaults(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	plain := databaseSchema("shop")
	createValid(t, c, plain)
	if err := c.Get(ctx, client.ObjectKeyFromObject(plain), plain); err != nil {
		t.Fatal(err)
	}
	if plain.Spec.DeletionPolicy != "Retain" {
		t.Fatalf("deletionPolicy %q", plain.Spec.DeletionPolicy)
	}
	plain.Status.Name = "shop"
	if err := c.Status().Update(ctx, plain); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	named := databaseSchema("shop-data")
	named.Spec.Name = "shop_data"
	named.Spec.DeletionPolicy = "Delete"
	createValid(t, c, named)

	badPolicy := databaseSchema("bad-policy")
	badPolicy.Spec.DeletionPolicy = "Archive"
	createInvalid(t, c, badPolicy, "spec.deletionPolicy")
	badName := databaseSchema("bad-name")
	badName.Spec.Name = "Shop-Data"
	createInvalid(t, c, badName, "spec.name")
	noRef := databaseSchema("no-ref")
	noRef.Spec.DatabaseRef = ""
	createInvalid(t, c, noRef, "spec.databaseRef")
}

func TestDatabaseSchemaReferenceAndNameAreImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)

	schema := databaseSchema("shop")
	schema.Spec.Name = "shop_data"
	createValid(t, c, schema)

	moved := schema.DeepCopy()
	moved.Spec.DatabaseRef = "other"
	updateInvalid(t, c, moved, "databaseRef is immutable")
	renamed := schema.DeepCopy()
	renamed.Spec.Name = "shop_other"
	updateInvalid(t, c, renamed, "name is immutable")

	policy := schema.DeepCopy()
	policy.Spec.DeletionPolicy = "Delete"
	if err := c.Update(context.Background(), policy); err != nil {
		t.Fatalf("deletionPolicy stays mutable: %v", err)
	}
}
