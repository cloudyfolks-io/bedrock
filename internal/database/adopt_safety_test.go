package database

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func existingAccountEngine() *fakeEngine {
	return &fakeEngine{handle: func(script string) (string, error) {
		switch {
		case strings.Contains(script, "CREATE USER 'carol'"):
			return "", engine.ErrExists
		case strings.Contains(script, "ACL GETUSER carol"):
			return "user carol on\\n", nil
		}
		return "", nil
	}}
}

func TestAPasswordSecretOfAnotherProjectIsNeverRead(t *testing.T) {
	c, fake := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	other := projectNamespace(t, c)
	passwordSecret(t, c, other, "shared-name", chosenPassword)
	user := newUser(t, c, db, "bob", withPasswordRef("shared-name"))
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonPasswordSecretNotFound || len(fake.scripts) != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}

func TestAnExistingValkeyAccountWithoutTheAnnotationGivesNameTaken(t *testing.T) {
	c := startEnv(t)
	fake := existingAccountEngine()
	db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineValkey)
	user := newUser(t, c, db, "carol", func(u *v1alpha1.DatabaseUser) { u.Spec.KeyPrefix = "p:" })
	reconcileUserOf(t, c, fake, v1alpha1.DatabaseEngineValkey, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || fake.occurrences("ACL SETUSER carol") != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}

func TestAChosenPasswordNeverLeavesTheSecretForMySQLAndValkey(t *testing.T) {
	for _, kind := range []string{v1alpha1.DatabaseEngineMySQL, v1alpha1.DatabaseEngineValkey} {
		c, fake := startEnv(t), &fakeEngine{}
		db := readyEngineDatabase(t, c, kind)
		passwordSecret(t, c, db.Namespace, "carol-chosen", chosenPassword)
		user := newUser(t, c, db, "carol", func(u *v1alpha1.DatabaseUser) {
			if kind == v1alpha1.DatabaseEngineValkey {
				u.Spec.KeyPrefix = "p:"
			}
			withPasswordRef("carol-chosen")(u)
		})
		reconcileUserOf(t, c, fake, kind, user)
		reloadUser(t, c, user)
		if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || len(fake.scripts) == 0 {
			t.Fatalf("%s: reason %s", kind, reason)
		}
		status, err := json.Marshal(user.Status)
		must(t, err)
		if strings.Contains(string(status), chosenPassword) {
			t.Fatalf("%s: the password is in the status", kind)
		}
		for _, script := range fake.scripts {
			if strings.Contains(script, chosenPassword) {
				t.Fatalf("%s: the password is in a script: %s", kind, script)
			}
		}
		var events corev1.EventList
		must(t, c.List(context.Background(), &events))
		for _, event := range events.Items {
			if strings.Contains(event.Message, chosenPassword) {
				t.Fatalf("%s: the password is in an event", kind)
			}
		}
	}
}

func TestAManagedUserAndSchemaKeepWorkingWithoutTheAnnotation(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileUser(t, c, fake, user)
	reconcileSchema(t, c, fake, schema)
	existing := existsOn("CREATE ROLE")
	existingDatabase := existsOn("CREATE DATABASE")
	fake.handle = func(script string) (string, error) {
		if out, err := existing(script); err != nil {
			return out, err
		}
		return existingDatabase(script)
	}
	reconcileUser(t, c, fake, user)
	reconcileSchema(t, c, fake, schema)
	if reason, _ := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonReady {
		t.Fatalf("user reason %s", reason)
	}
	if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonReady || schema.Status.Name != "orders" {
		t.Fatalf("schema reason %s status %+v", reason, schema.Status)
	}
}

func TestAnUpgradeSeedsThePasswordDigestWithoutAnEngineCall(t *testing.T) {
	for _, kind := range []string{v1alpha1.DatabaseEngineMySQL, v1alpha1.DatabaseEngineValkey} {
		c, fake := startEnv(t), &fakeEngine{}
		db := readyEngineDatabase(t, c, kind)
		user := newUser(t, c, db, "alice", func(u *v1alpha1.DatabaseUser) {
			if kind == v1alpha1.DatabaseEngineValkey {
				u.Spec.KeyPrefix = "p:"
			}
		})
		reconcileUserOf(t, c, fake, kind, user)
		password := string(getSecret(t, c, db.Namespace, "alice-db").Data["password"])
		reloadUser(t, c, user).Status.PasswordDigest = ""
		must(t, c.Status().Update(context.Background(), user))
		fake.reset()
		reconcileUserOf(t, c, fake, kind, user)
		if len(fake.scripts) != 0 || !passwordSynced(t, c, user, password) {
			t.Fatalf("%s: scripts %v digest %q", kind, fake.scripts, user.Status.PasswordDigest)
		}
	}
}
