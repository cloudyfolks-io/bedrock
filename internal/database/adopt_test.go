package database

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
)

const chosenPassword = "Chosen-Passw0rd!x#2026"

func existsOn(fragment string) func(string) (string, error) {
	return func(script string) (string, error) {
		if strings.Contains(script, fragment) {
			return "", &exec.CommandError{Kind: exec.KindExists, ExitCode: 3}
		}
		if strings.Contains(script, "FROM pg_database") {
			return "app\n", nil
		}
		return "", nil
	}
}

func adoptAnnotation() map[string]string {
	return map[string]string{v1alpha1.AnnotationAdopt: "true"}
}

func passwordSecret(t *testing.T, c client.Client, namespace, name, password string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string][]byte{"password": []byte(password)}}
	create(t, c, secret)
	return secret
}

func withPasswordRef(name string) func(*v1alpha1.DatabaseUser) {
	return func(user *v1alpha1.DatabaseUser) {
		user.Spec.PasswordSecretRef = &v1alpha1.DatabaseSecretRef{Name: name}
	}
}

func withUserAnnotations(annotations map[string]string) func(*v1alpha1.DatabaseUser) {
	return func(user *v1alpha1.DatabaseUser) { user.Annotations = annotations }
}

func TestAnExistingPostgresRoleWithoutTheAnnotationGivesNameTaken(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || user.Status.AppliedGrants != "" {
		t.Fatalf("reason %s status %+v", reason, user.Status)
	}
	if fake.count("ALTER ROLE") != 0 || fake.count("REVOKE") != 0 {
		t.Fatalf("the role was changed: %v", fake.scripts)
	}
}

func TestAnExistingMySQLUserWithoutTheAnnotationGivesNameTaken(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE USER") {
			return "", engine.ErrExists
		}
		return "", nil
	}}
	db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineMySQL)
	user := newUser(t, c, db, "carol", nil)
	reconcileUserOf(t, c, fake, v1alpha1.DatabaseEngineMySQL, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || fake.count("ALTER USER") != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}

func TestAnAdoptedRoleKeepsItsPasswordAndTheSecretHoldsNone(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", withUserAnnotations(adoptAnnotation()))
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
	if fake.count("PASSWORD") > 1 || fake.count(`ALTER ROLE "alice" PASSWORD`) != 0 || fake.count(`REVOKE "app_ro", "app_rw", "app_all" FROM "alice"`) != 1 {
		t.Fatalf("scripts %v", fake.scripts)
	}
	secret := getSecret(t, c, db.Namespace, "alice-db")
	if len(secret.Data["password"]) != 0 || secret.Annotations[annotationPasswordSynced] != "true" || strings.Contains(string(secret.Data["uri"]), "@@") {
		t.Fatalf("secret %v %v", secret.Data, secret.Annotations)
	}
	fake.reset()
	reconcileUser(t, c, fake, user)
	if len(fake.scripts) != 0 || len(getSecret(t, c, db.Namespace, "alice-db").Data["password"]) != 0 {
		t.Fatalf("a second pass changed the role: %v", fake.scripts)
	}
}

func TestAnAdoptedRoleTakesTheChosenPassword(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	passwordSecret(t, c, db.Namespace, "alice-chosen", chosenPassword)
	user := newUser(t, c, db, "alice", func(u *v1alpha1.DatabaseUser) {
		withUserAnnotations(adoptAnnotation())(u)
		withPasswordRef("alice-chosen")(u)
	})
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady {
		t.Fatalf("status %+v", user.Status)
	}
	if fake.count(`ALTER ROLE "alice" PASSWORD 'SCRAM-SHA-256$`) != 1 {
		t.Fatalf("scripts %v", fake.scripts)
	}
	for _, script := range fake.scripts {
		if strings.Contains(script, chosenPassword) {
			t.Fatal("the chosen password is in a script")
		}
	}
	secret := getSecret(t, c, db.Namespace, "alice-db")
	if string(secret.Data["password"]) != chosenPassword || secret.Annotations[annotationPasswordSynced] != "true" {
		t.Fatalf("secret %v", secret.Annotations)
	}
}

func TestANewRoleTakesTheChosenPasswordAndNoRandomOne(t *testing.T) {
	c, fake := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	passwordSecret(t, c, db.Namespace, "bob-chosen", chosenPassword)
	user := newUser(t, c, db, "bob", withPasswordRef("bob-chosen"))
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	secret := getSecret(t, c, db.Namespace, "bob-db")
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || string(secret.Data["password"]) != chosenPassword {
		t.Fatalf("status %+v", user.Status)
	}
	if !strings.Contains(string(secret.Data["uri"]), "Chosen-Passw0rd") {
		t.Fatalf("uri %s", secret.Data["uri"])
	}
	for _, script := range fake.scripts {
		if strings.Contains(script, chosenPassword) {
			t.Fatal("the chosen password is in a script")
		}
	}
}

func TestAChangedChosenPasswordIsSetInTheEngine(t *testing.T) {
	c, fake := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	source := passwordSecret(t, c, db.Namespace, "bob-chosen", chosenPassword)
	user := newUser(t, c, db, "bob", withPasswordRef("bob-chosen"))
	reconcileUser(t, c, fake, user)
	fake.reset()
	source.Data["password"] = []byte("Another-Passw0rd!x#2027")
	must(t, c.Update(context.Background(), source))
	reconcileUser(t, c, fake, user)
	secret := getSecret(t, c, db.Namespace, "bob-db")
	if string(secret.Data["password"]) != "Another-Passw0rd!x#2027" || fake.count(`ALTER ROLE "bob" PASSWORD 'SCRAM-SHA-256$`) != 1 || secret.Annotations[annotationPasswordSynced] != "true" {
		t.Fatalf("scripts %v annotations %v", fake.scripts, secret.Annotations)
	}
}

func TestTheChosenPasswordWinsOverAResetToken(t *testing.T) {
	c, fake := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	passwordSecret(t, c, db.Namespace, "bob-chosen", chosenPassword)
	user := newUser(t, c, db, "bob", func(u *v1alpha1.DatabaseUser) {
		withPasswordRef("bob-chosen")(u)
		withUserAnnotations(map[string]string{v1alpha1.AnnotationResetPassword: "one"})(u)
	})
	reconcileUser(t, c, fake, user)
	if string(getSecret(t, c, db.Namespace, "bob-db").Data["password"]) != chosenPassword {
		t.Fatal("a reset token replaced the chosen password")
	}
}

func TestAMissingPasswordSecretBlocksTheUserWithoutSQL(t *testing.T) {
	c, fake := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "bob", withPasswordRef("absent"))
	reconcileUser(t, c, fake, user)
	if reason, _ := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonPasswordSecretNotFound || len(fake.scripts) != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}

func TestAnInvalidChosenPasswordIsRefusedWithoutEchoingIt(t *testing.T) {
	cases := map[string]string{
		"short":   "Sh0rt!pw",
		"space":   "Has a space 0123456789",
		"quote":   "Quote'd-Passw0rd-0123",
		"unicode": "Pässw0rd-with-umlaut-0123",
		"long":    strings.Repeat("a", 129),
		"empty":   "",
	}
	for name, password := range cases {
		c, fake := startEnv(t), &fakeEngine{}
		db := readyDatabase(t, c)
		passwordSecret(t, c, db.Namespace, "pw", password)
		user := newUser(t, c, db, "bob", withPasswordRef("pw"))
		reconcileUser(t, c, fake, user)
		reloadUser(t, c, user)
		reason, _ := readyReasonOf(user.Status.Conditions)
		message := ""
		for _, condition := range user.Status.Conditions {
			message += condition.Message
		}
		if reason != ReasonInvalidSpec || len(fake.scripts) != 0 || (password != "" && strings.Contains(message, password)) {
			t.Fatalf("%s: reason %s message %q scripts %v", name, reason, message, fake.scripts)
		}
	}
}

func TestOurOwnCreateIsNotMistakenForAForeignRoleAfterACrash(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	user.Status.AppliedGrants = ""
	must(t, c.Status().Update(context.Background(), user))
	fake.handle = existsOn("CREATE ROLE")
	reconcileUser(t, c, fake, user)
	if reason, _ := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonReady || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
}

func TestAnExistingPostgresDatabaseWithoutTheAnnotationGivesNameTaken(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE DATABASE")}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileSchema(t, c, fake, schema)
	if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonNameTaken || schema.Status.Name != "" {
		t.Fatalf("reason %s status %+v", reason, schema.Status)
	}
	if len(fake.scripts) != 1 {
		t.Fatalf("more than the create ran: %v", fake.scripts)
	}
}

func TestAnExistingMySQLDatabaseWithoutTheAnnotationGivesNameTaken(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE DATABASE") {
			return "", engine.ErrExists
		}
		return "", nil
	}}
	db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineMySQL)
	schema := &v1alpha1.DatabaseSchema{ObjectMeta: metav1.ObjectMeta{Name: "reports", Namespace: db.Namespace}, Spec: v1alpha1.DatabaseSchemaSpec{DatabaseRef: db.Name}}
	create(t, c, schema)
	_, err := (&SchemaReconciler{Client: c, Engine: fake.factoryOf(v1alpha1.DatabaseEngineMySQL)}).Reconcile(context.Background(), requestOf(schema))
	must(t, err)
	must(t, c.Get(context.Background(), requestOf(schema).NamespacedName, schema))
	if reason, _ := readyReasonOf(schema.Status.Conditions); reason != ReasonNameTaken || schema.Status.Name != "" {
		t.Fatalf("reason %s name %q", reason, schema.Status.Name)
	}
}

func TestAnAdoptedPostgresDatabaseGetsItsGroupRoles(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE DATABASE")}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", func(s *v1alpha1.DatabaseSchema) { s.Annotations = adoptAnnotation() })
	reconcileSchema(t, c, fake, schema)
	if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonReady || schema.Status.Name != "orders" {
		t.Fatalf("status %+v", schema.Status)
	}
}

func secretMarker(t *testing.T, c client.Client, namespace string) (string, bool) {
	t.Helper()
	secret := getSecret(t, c, namespace, "alice-db")
	value, found := secret.Annotations[annotationRoleIntent]
	return value, found
}

func TestARoleThatExistsWithoutTheIntentMarkerIsNeverTouched(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || fake.count("ALTER ROLE") != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
	if _, found := secretMarker(t, c, db.Namespace); found {
		t.Fatal("the intent marker stays after the refusal")
	}
}

func TestTheIntentMarkerIsWrittenBeforeTheCreateAndRemovedAfterTheSync(t *testing.T) {
	c := startEnv(t)
	db := readyDatabase(t, c)
	var user *v1alpha1.DatabaseUser
	var markerAtCreate string
	fake := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE ROLE") {
			markerAtCreate, _ = secretMarker(t, c, db.Namespace)
		}
		return "", nil
	}}
	user = newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if markerAtCreate != string(user.UID) {
		t.Fatalf("marker at the create %q, user %q", markerAtCreate, user.UID)
	}
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady {
		t.Fatalf("reason %s", reason)
	}
	secret := getSecret(t, c, db.Namespace, "alice-db")
	if _, found := secret.Annotations[annotationRoleIntent]; found || secret.Annotations[annotationPasswordSynced] != "true" {
		t.Fatalf("annotations %v", secret.Annotations)
	}
}

func markIntent(t *testing.T, c client.Client, namespace, value string) {
	t.Helper()
	secret := getSecret(t, c, namespace, "alice-db")
	secret.Annotations[annotationRoleIntent] = value
	must(t, c.Update(context.Background(), &secret))
}

func TestARoleWithOurIntentMarkerGetsTheSecretPassword(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	password := string(getSecret(t, c, db.Namespace, "alice-db").Data["password"])
	markIntent(t, c, db.Namespace, string(user.UID))
	fake.reset()
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
	after := getSecret(t, c, db.Namespace, "alice-db")
	_, found := after.Annotations[annotationRoleIntent]
	if fake.count(`ALTER ROLE "alice" PASSWORD 'SCRAM-SHA-256$`) != 1 || string(after.Data["password"]) != password || after.Annotations[annotationPasswordSynced] != "true" || found {
		t.Fatalf("scripts %v secret %v", fake.scripts, after.Annotations)
	}
}

func TestARoleWithAnotherIntentMarkerIsNeverTouched(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	markIntent(t, c, db.Namespace, "another-uid")
	fake.reset()
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || fake.count("ALTER ROLE") != 0 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}

func TestAForeignRoleThatAppearsBeforeTheCreateRemovesTheMarker(t *testing.T) {
	c := startEnv(t)
	db := readyDatabase(t, c)
	fake := &fakeEngine{handle: existsOn("CREATE ROLE")}
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, fake, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || user.Status.AppliedGrants != "" {
		t.Fatalf("status %+v", user.Status)
	}
	if _, found := secretMarker(t, c, db.Namespace); found || fake.count("ALTER ROLE") != 0 {
		t.Fatalf("marker found %v scripts %v", found, fake.scripts)
	}
}

func TestAMySQLAccountWithOurIntentMarkerGetsTheSecretPassword(t *testing.T) {
	c := startEnv(t)
	fake := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE USER") {
			return "", engine.ErrExists
		}
		return "", nil
	}}
	db := readyEngineDatabase(t, c, v1alpha1.DatabaseEngineMySQL)
	user := newUser(t, c, db, "alice", nil)
	reconcileUserOf(t, c, fake, v1alpha1.DatabaseEngineMySQL, user)
	markIntent(t, c, db.Namespace, string(user.UID))
	fake.reset()
	reconcileUserOf(t, c, fake, v1alpha1.DatabaseEngineMySQL, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || fake.count("ALTER USER") != 1 {
		t.Fatalf("reason %s scripts %v", reason, fake.scripts)
	}
}
