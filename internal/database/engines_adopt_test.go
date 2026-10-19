package database

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func TestExistingMySQLAndValkeyAccountsAreAdopted(t *testing.T) {
	c := startEnv(t)
	cases := []struct {
		kind, fragment string
		exists         error
		edit           func(*v1alpha1.DatabaseUser)
	}{
		{v1alpha1.DatabaseEngineMySQL, "ALTER USER 'carol'", engine.ErrExists, nil},
		{v1alpha1.DatabaseEngineValkey, "ACL SETUSER carol", nil, func(user *v1alpha1.DatabaseUser) { user.Spec.KeyPrefix = "p:" }},
	}
	for _, tc := range cases {
		fake := &fakeEngine{handle: func(script string) (string, error) {
			if strings.Contains(script, "CREATE USER 'carol'") {
				return "", engine.ErrExists
			}
			if strings.Contains(script, "ACL GETUSER carol") {
				return "user carol on\\n", nil
			}
			return "", nil
		}}
		db := readyEngineDatabase(t, c, tc.kind)
		user := newUser(t, c, db, "carol", tc.edit)
		reconcileUserOf(t, c, fake, tc.kind, user)
		reloadUser(t, c, user)
		if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || fake.occurrences(tc.fragment) == 0 {
			t.Fatalf("%s: reason %s scripts %v", tc.kind, reason, fake.scripts)
		}
	}
}

func TestAnExistingMySQLDatabaseIsAdoptedBySchema(t *testing.T) {
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
	if reason, _ := readyReasonOf(schema.Status.Conditions); reason != ReasonReady || schema.Status.Name != "reports" {
		t.Fatalf("reason %s name %q", reason, schema.Status.Name)
	}
}

func TestAUserSecretThatTheUserDoesNotControlGivesNameTakenForEveryEngine(t *testing.T) {
	c := startEnv(t)
	for _, kind := range []string{v1alpha1.DatabaseEngineMySQL, v1alpha1.DatabaseEngineValkey} {
		fake := &fakeEngine{}
		db := readyEngineDatabase(t, c, kind)
		create(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dave-db", Namespace: db.Namespace}, Data: map[string][]byte{"x": []byte("y")}})
		user := newUser(t, c, db, "dave", nil)
		reconcileUserOf(t, c, fake, kind, user)
		reloadUser(t, c, user)
		if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonNameTaken || len(fake.scripts) != 0 {
			t.Fatalf("%s: reason %s scripts %v", kind, reason, fake.scripts)
		}
	}
}

func TestARunningDatabaseRefusesAVersionThatIsNoUpgradeTarget(t *testing.T) {
	w := valkeyWorld(t)
	w.bringUpValkey()
	w.addVersion(v1alpha1.DatabaseVersionSpec{Engine: "valkey", Major: "9.1", Image: valkeyImageNew, Port: 6379, Enabled: true, Companions: map[string]string{"exporter": exporterImage}})
	w.db.Spec.Version = "9.1"
	must(t, w.c.Update(context.Background(), w.db))
	w.reconcile()
	if w.reason() != ReasonVersionNotAllowed || w.valkeyDeployment().Spec.Template.Spec.Containers[0].Image != valkeyImageOld {
		t.Fatalf("reason %s", w.reason())
	}
}
