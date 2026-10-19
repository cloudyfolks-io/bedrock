package database

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
)

type fakeEngine struct {
	mu      sync.Mutex
	scripts []string
	handle  func(script string) (string, error)
}

func (f *fakeEngine) run(_ context.Context, script string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, script)
	if f.handle != nil {
		return f.handle(script)
	}
	if strings.Contains(script, "FROM pg_database") {
		return "app\norders\n", nil
	}
	return "", nil
}

func (f *fakeEngine) factory() EngineFactory {
	return func(context.Context, v1alpha1.Database) (Engine, error) {
		return Engine{Run: f.run, Timeout: 2 * time.Minute}, nil
	}
}

func (f *fakeEngine) count(fragment string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, script := range f.scripts {
		if strings.Contains(script, fragment) {
			n++
		}
	}
	return n
}

func (f *fakeEngine) occurrences(fragment string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Count(strings.Join(f.scripts, "\n"), fragment)
}

func (f *fakeEngine) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = nil
}

func readyDatabase(t *testing.T, c client.Client) *v1alpha1.Database {
	t.Helper()
	namespace := projectNamespace(t, c)
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: namespace}, Spec: newSpec()}
	create(t, c, db)
	markEngineReady(t, c, db, true)
	return db
}

func markEngineReady(t *testing.T, c client.Client, db *v1alpha1.Database, ready bool) {
	t.Helper()
	ctx := context.Background()
	must(t, c.Get(ctx, client.ObjectKeyFromObject(db), db))
	status := metav1.ConditionTrue
	if !ready {
		status = metav1.ConditionFalse
	}
	v1alpha1.SetCondition(&db.Status.Conditions, metav1.Condition{Type: ConditionEngineReady, Status: status, Reason: "Test"})
	db.Status.Addresses = []v1alpha1.DatabaseAddress{{Subnet: "default", IP: "10.100.0.2", Port: 5432}}
	must(t, c.Status().Update(ctx, db))
}

func newUser(t *testing.T, c client.Client, db *v1alpha1.Database, name string, edit func(*v1alpha1.DatabaseUser)) *v1alpha1.DatabaseUser {
	t.Helper()
	user := &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: db.Namespace},
		Spec:       v1alpha1.DatabaseUserSpec{DatabaseRef: db.Name, Grants: []v1alpha1.DatabaseGrant{{Database: "app", Level: "readwrite"}}},
	}
	if edit != nil {
		edit(user)
	}
	create(t, c, user)
	return user
}

func reconcileUser(t *testing.T, c client.Client, engine *fakeEngine, user *v1alpha1.DatabaseUser) ctrl.Result {
	t.Helper()
	result, err := (&UserReconciler{Client: c, Engine: engine.factory()}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(user)})
	must(t, err)
	return result
}

func reloadUser(t *testing.T, c client.Client, user *v1alpha1.DatabaseUser) *v1alpha1.DatabaseUser {
	t.Helper()
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(user), user))
	return user
}

func readyReasonOf(conditions []metav1.Condition) (string, metav1.ConditionStatus) {
	for _, condition := range conditions {
		if condition.Type == v1alpha1.ConditionReady {
			return condition.Reason, condition.Status
		}
	}
	return "", ""
}

func TestUserIsMadeWithCredentialsAndAVerifier(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	reloadUser(t, c, user)

	reason, status := readyReasonOf(user.Status.Conditions)
	if reason != ReasonReady || status != metav1.ConditionTrue || user.Status.Username != "alice" || user.Status.CredentialsSecret != "alice-db" || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
	if !controllerutil.ContainsFinalizer(user, finalizerUser) {
		t.Fatal("no finalizer")
	}
	secret := getSecret(t, c, db.Namespace, "alice-db")
	password := string(secret.Data["password"])
	if len(password) != 32 || string(secret.Data["username"]) != "alice" || string(secret.Data["host"]) != "10.100.0.2" || string(secret.Data["port"]) != "5432" ||
		string(secret.Data["uri"]) != "postgresql://alice:"+password+"@10.100.0.2:5432/app" {
		t.Fatalf("secret %v", secret.Data)
	}
	if len(secret.OwnerReferences) != 1 || secret.Labels[v1alpha1.LabelKind] != "DatabaseUser" || secret.Annotations[annotationPasswordSynced] != "true" {
		t.Fatalf("secret meta %v %v", secret.OwnerReferences, secret.Annotations)
	}
	if engine.count(`CREATE ROLE "alice" LOGIN`) != 1 || engine.count(`GRANT "app_rw" TO "alice"`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
	for _, script := range engine.scripts {
		if strings.Contains(script, password) {
			t.Fatal("the password is in a script")
		}
	}
	engine.reset()
	reconcileUser(t, c, engine, user)
	if len(engine.scripts) != 0 {
		t.Fatalf("a second reconcile ran %v", engine.scripts)
	}
}

func TestUserNameRules(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	hyphen := newUser(t, c, db, "alice-b", nil)
	reconcileUser(t, c, engine, hyphen)
	if reason, _ := readyReasonOf(reloadUser(t, c, hyphen).Status.Conditions); reason != ReasonInvalidSpec {
		t.Fatalf("hyphen name: %s", reason)
	}
	named := newUser(t, c, db, "alice-c", func(u *v1alpha1.DatabaseUser) { u.Spec.Username = "alice_c" })
	reconcileUser(t, c, engine, named)
	if reason, _ := readyReasonOf(reloadUser(t, c, named).Status.Conditions); reason != ReasonReady || named.Status.Username != "alice_c" {
		t.Fatalf("spec.username: %s %+v", reason, named.Status)
	}
	reserved := newUser(t, c, db, "postgres", nil)
	reconcileUser(t, c, engine, reserved)
	if reason, _ := readyReasonOf(reloadUser(t, c, reserved).Status.Conditions); reason != ReasonInvalidSpec {
		t.Fatalf("reserved name: %s", reason)
	}
	if engine.count(`"alice-b"`) != 0 || engine.count(`ROLE "postgres"`) != 0 {
		t.Fatalf("scripts ran for a refused name: %v", engine.scripts)
	}
}

func TestUserWaitsForTheDatabaseTheAddressAndTheGrantDatabase(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)

	orphan := &v1alpha1.DatabaseUser{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: db.Namespace}, Spec: v1alpha1.DatabaseUserSpec{DatabaseRef: "missing"}}
	create(t, c, orphan)
	reconcileUser(t, c, engine, orphan)
	if reason, _ := readyReasonOf(reloadUser(t, c, orphan).Status.Conditions); reason != ReasonDatabaseNotFound {
		t.Fatalf("missing database: %s", reason)
	}

	markEngineReady(t, c, db, false)
	waiting := newUser(t, c, db, "bob", nil)
	reconcileUser(t, c, engine, waiting)
	if reason, _ := readyReasonOf(reloadUser(t, c, waiting).Status.Conditions); reason != ReasonDatabaseNotReady {
		t.Fatalf("engine not ready: %s", reason)
	}
	markEngineReady(t, c, db, true)
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), db))
	db.Status.Addresses = nil
	must(t, c.Status().Update(context.Background(), db))
	reconcileUser(t, c, engine, waiting)
	if reason, _ := readyReasonOf(reloadUser(t, c, waiting).Status.Conditions); reason != ReasonEndpointPending {
		t.Fatalf("no address: %s", reason)
	}
	markEngineReady(t, c, db, true)

	carol := newUser(t, c, db, "carol", func(u *v1alpha1.DatabaseUser) {
		u.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "orders", Level: "readonly"}}
	})
	result := reconcileUser(t, c, engine, carol)
	if reason, _ := readyReasonOf(reloadUser(t, c, carol).Status.Conditions); reason != ReasonGrantDatabaseNotFound || result.RequeueAfter == 0 {
		t.Fatalf("grant database: %s %v", reason, result)
	}
	if engine.count("CREATE ROLE") != 0 {
		t.Fatal("a role was made before its grant database exists")
	}
	schema := &v1alpha1.DatabaseSchema{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: db.Namespace}, Spec: v1alpha1.DatabaseSchemaSpec{DatabaseRef: db.Name}}
	create(t, c, schema)
	reconcileSchema(t, c, engine, schema)
	reconcileUser(t, c, engine, carol)
	if reason, _ := readyReasonOf(reloadUser(t, c, carol).Status.Conditions); reason != ReasonReady {
		t.Fatalf("after the schema is ready: %s", reason)
	}
	secret := getSecret(t, c, db.Namespace, "carol-db")
	if !strings.HasSuffix(string(secret.Data["uri"]), "@10.100.0.2:5432/orders") {
		t.Fatalf("uri %s", secret.Data["uri"])
	}
}

func TestUserGrantChangeRunsOnlyTheGrantScript(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	engine.reset()
	reloadUser(t, c, user)
	user.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "app", Level: "readonly"}}
	must(t, c.Update(context.Background(), user))
	reconcileUser(t, c, engine, user)
	if engine.count("CREATE ROLE") != 0 || engine.count(`REVOKE "app_ro", "app_rw", "app_all" FROM "alice"`) != 1 || engine.count(`GRANT "app_ro" TO "alice"`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
	engine.reset()
	reconcileUser(t, c, engine, user)
	if len(engine.scripts) != 0 {
		t.Fatalf("a second reconcile ran %v", engine.scripts)
	}
}

func TestUserPasswordResetFollowsTheAnnotationOnly(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	before := string(getSecret(t, c, db.Namespace, "alice-db").Data["password"])
	engine.reset()

	reloadUser(t, c, user)
	user.Annotations = map[string]string{v1alpha1.AnnotationResetPassword: "one"}
	must(t, c.Update(context.Background(), user))
	reconcileUser(t, c, engine, user)
	secret := getSecret(t, c, db.Namespace, "alice-db")
	after := string(secret.Data["password"])
	if after == before || secret.Annotations[annotationPasswordSynced] != "true" || engine.count(`ALTER ROLE "alice" PASSWORD 'SCRAM-SHA-256$`) != 1 {
		t.Fatalf("reset: password changed %v, scripts %v", after != before, engine.scripts)
	}
	for _, script := range engine.scripts {
		if strings.Contains(script, after) {
			t.Fatal("the new password is in a script")
		}
	}
	engine.reset()
	reconcileUser(t, c, engine, user)
	if len(engine.scripts) != 0 || string(getSecret(t, c, db.Namespace, "alice-db").Data["password"]) != after {
		t.Fatalf("the same token reset again: %v", engine.scripts)
	}
}

func TestUserAdoptsARoleThatAlreadyExists(t *testing.T) {
	c := startEnv(t)
	engine := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE ROLE") {
			return "", &exec.CommandError{Kind: exec.KindExists, ExitCode: 3}
		}
		if strings.Contains(script, "FROM pg_database") {
			return "app\n", nil
		}
		return "", nil
	}}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonReady || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
	if engine.count("ALTER ROLE") != 1 || engine.count(`REVOKE "app_ro", "app_rw", "app_all" FROM "alice"`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
}

func TestUserReportsAnUnavailableEngineWithoutAnError(t *testing.T) {
	c := startEnv(t)
	engine := &fakeEngine{handle: func(string) (string, error) { return "", exec.ErrNotReady }}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	result := reconcileUser(t, c, engine, user)
	if reason, status := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonEngineUnavailable || status != metav1.ConditionFalse || result.RequeueAfter == 0 {
		t.Fatalf("reason %s %s %v", reason, status, result)
	}
	if user.Status.AppliedGrants != "" {
		t.Fatal("grants recorded after a failure")
	}
	engine.handle = nil
	reconcileUser(t, c, engine, user)
	if reason, _ := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonReady {
		t.Fatalf("the retry did not finish: %s", reason)
	}
}

func deleteAndReconcileUser(t *testing.T, c client.Client, engine *fakeEngine, user *v1alpha1.DatabaseUser) {
	t.Helper()
	must(t, c.Delete(context.Background(), user))
	reconcileUser(t, c, engine, user)
}

func userGone(c client.Client, user *v1alpha1.DatabaseUser) bool {
	err := c.Get(context.Background(), client.ObjectKeyFromObject(user), &v1alpha1.DatabaseUser{})
	return apierrors.IsNotFound(err)
}

func TestUserDeleteDropsTheRoleAndMovesItsObjects(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	engine.reset()
	deleteAndReconcileUser(t, c, engine, user)
	if engine.occurrences(`REASSIGN OWNED BY "alice" TO "dbadmin"`) != 3 || engine.count(`DROP ROLE "alice"`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
	eventually(t, "the user to go away", func() bool { return userGone(c, user) })
}

func TestUserDeleteKeepsTheFinalizerWhileTheRoleStillOwnsObjects(t *testing.T) {
	c := startEnv(t)
	engine := &fakeEngine{}
	db := readyDatabase(t, c)
	user := newUser(t, c, db, "alice", nil)
	reconcileUser(t, c, engine, user)
	engine.handle = func(script string) (string, error) {
		if strings.Contains(script, "DROP ROLE") {
			return "", &exec.CommandError{Kind: exec.KindOwnsObjects, ExitCode: 3}
		}
		return "app\n", nil
	}
	deleteAndReconcileUser(t, c, engine, user)
	reloadUser(t, c, user)
	if reason, _ := readyReasonOf(user.Status.Conditions); reason != ReasonUserOwnsObjects || userGone(c, user) {
		t.Fatalf("reason %s gone %v", reason, userGone(c, user))
	}
	engine.handle = nil
	reconcileUser(t, c, engine, user)
	eventually(t, "the user to go away", func() bool { return userGone(c, user) })
}

func TestUserDeleteWithoutARoleOrWithoutADatabaseNeedsNoSQL(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	never := newUser(t, c, db, "never", nil)
	reconcileUser(t, c, engine, never)
	engine.reset()
	markEngineReady(t, c, db, false)
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(never), never))
	never.Status.AppliedGrants = ""
	must(t, c.Status().Update(context.Background(), never))
	deleteAndReconcileUser(t, c, engine, never)
	eventually(t, "the never-created user to go away", func() bool { return userGone(c, never) })

	markEngineReady(t, c, db, true)
	other := newUser(t, c, db, "other", nil)
	reconcileUser(t, c, engine, other)
	engine.reset()
	must(t, c.Delete(context.Background(), db))
	deleteAndReconcileUser(t, c, engine, other)
	eventually(t, "the user of a deleted database to go away", func() bool { return userGone(c, other) })
	if len(engine.scripts) != 0 {
		t.Fatalf("SQL ran: %v", engine.scripts)
	}
}

func reconcileSchema(t *testing.T, c client.Client, engine *fakeEngine, schema *v1alpha1.DatabaseSchema) ctrl.Result {
	t.Helper()
	result, err := (&SchemaReconciler{Client: c, Engine: engine.factory()}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schema)})
	must(t, err)
	return result
}

func newSchema(t *testing.T, c client.Client, db *v1alpha1.Database, name string, edit func(*v1alpha1.DatabaseSchema)) *v1alpha1.DatabaseSchema {
	t.Helper()
	schema := &v1alpha1.DatabaseSchema{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: db.Namespace}, Spec: v1alpha1.DatabaseSchemaSpec{DatabaseRef: db.Name}}
	if edit != nil {
		edit(schema)
	}
	create(t, c, schema)
	return schema
}

func reloadSchema(t *testing.T, c client.Client, schema *v1alpha1.DatabaseSchema) *v1alpha1.DatabaseSchema {
	t.Helper()
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(schema), schema))
	return schema
}

func schemaGone(c client.Client, schema *v1alpha1.DatabaseSchema) bool {
	return apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(schema), &v1alpha1.DatabaseSchema{}))
}

func TestSchemaMakesTheDatabaseOnceAndDefaultsToRetain(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileSchema(t, c, engine, schema)
	reloadSchema(t, c, schema)
	reason, status := readyReasonOf(schema.Status.Conditions)
	if reason != ReasonReady || status != metav1.ConditionTrue || schema.Status.Name != "orders" || schema.Spec.DeletionPolicy != "Retain" {
		t.Fatalf("status %+v policy %s", schema.Status, schema.Spec.DeletionPolicy)
	}
	if engine.count(`CREATE DATABASE "orders"`) != 1 || engine.count(`CREATE ROLE "orders_ro" NOLOGIN`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
	engine.reset()
	reconcileSchema(t, c, engine, schema)
	if len(engine.scripts) != 0 {
		t.Fatalf("a second reconcile ran %v", engine.scripts)
	}
}

func TestSchemaNameRulesAndConflicts(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	for name, edit := range map[string]func(*v1alpha1.DatabaseSchema){
		"app":        nil,
		"postgres":   nil,
		"has-hyphen": nil,
	} {
		schema := newSchema(t, c, db, name, edit)
		reconcileSchema(t, c, engine, schema)
		if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonInvalidSpec {
			t.Errorf("%s: %s", name, reason)
		}
	}
	named := newSchema(t, c, db, "has-hyphen-too", func(s *v1alpha1.DatabaseSchema) { s.Spec.Name = "shop_data" })
	reconcileSchema(t, c, engine, named)
	if reason, _ := readyReasonOf(reloadSchema(t, c, named).Status.Conditions); reason != ReasonReady || named.Status.Name != "shop_data" {
		t.Fatalf("spec.name: %s %+v", reason, named.Status)
	}
	duplicate := newSchema(t, c, db, "shop-copy", func(s *v1alpha1.DatabaseSchema) { s.Spec.Name = "shop_data" })
	engine.reset()
	reconcileSchema(t, c, engine, duplicate)
	if reason, _ := readyReasonOf(reloadSchema(t, c, duplicate).Status.Conditions); reason != ReasonNameConflict || len(engine.scripts) != 0 {
		t.Fatalf("duplicate: %s %v", reason, engine.scripts)
	}
}

func TestSchemaAdoptsADatabaseThatAlreadyExists(t *testing.T) {
	c := startEnv(t)
	engine := &fakeEngine{handle: func(script string) (string, error) {
		if strings.Contains(script, "CREATE DATABASE") {
			return "", &exec.CommandError{Kind: exec.KindExists, ExitCode: 3}
		}
		return "", nil
	}}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileSchema(t, c, engine, schema)
	if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonReady || schema.Status.Name != "orders" {
		t.Fatalf("status %+v", schema.Status)
	}
}

func TestSchemaDeleteWithRetainRemovesTheObjectOnly(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileSchema(t, c, engine, schema)
	engine.reset()
	must(t, c.Delete(context.Background(), schema))
	reconcileSchema(t, c, engine, schema)
	eventually(t, "the schema to go away", func() bool { return schemaGone(c, schema) })
	if len(engine.scripts) != 0 {
		t.Fatalf("SQL ran for Retain: %v", engine.scripts)
	}
}

func TestSchemaDeleteWithDeleteDropsTheDatabase(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", func(s *v1alpha1.DatabaseSchema) { s.Spec.DeletionPolicy = "Delete" })
	reconcileSchema(t, c, engine, schema)
	engine.reset()
	must(t, c.Delete(context.Background(), schema))
	reconcileSchema(t, c, engine, schema)
	if engine.count(`DROP DATABASE IF EXISTS "orders" WITH (FORCE)`) != 1 || engine.count(`DROP ROLE IF EXISTS "orders_ro", "orders_rw", "orders_all"`) != 1 {
		t.Fatalf("scripts %v", engine.scripts)
	}
	eventually(t, "the schema to go away", func() bool { return schemaGone(c, schema) })
}

func TestSchemaDeleteWithDeleteAndAGoneDatabaseNeedsNoSQL(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	schema := newSchema(t, c, db, "orders", func(s *v1alpha1.DatabaseSchema) { s.Spec.DeletionPolicy = "Delete" })
	reconcileSchema(t, c, engine, schema)
	engine.reset()
	must(t, c.Delete(context.Background(), db))
	must(t, c.Delete(context.Background(), schema))
	reconcileSchema(t, c, engine, schema)
	eventually(t, "the schema to go away", func() bool { return schemaGone(c, schema) })
	if len(engine.scripts) != 0 {
		t.Fatalf("SQL ran: %v", engine.scripts)
	}
}

func TestSchemaWaitsForAReadyEngine(t *testing.T) {
	c, engine := startEnv(t), &fakeEngine{}
	db := readyDatabase(t, c)
	markEngineReady(t, c, db, false)
	schema := newSchema(t, c, db, "orders", nil)
	reconcileSchema(t, c, engine, schema)
	if reason, _ := readyReasonOf(reloadSchema(t, c, schema).Status.Conditions); reason != ReasonDatabaseNotReady || len(engine.scripts) != 0 {
		t.Fatalf("reason %s scripts %v", reason, engine.scripts)
	}
}

type stderrExecutor struct {
	stderr   string
	requests []exec.Request
}

func (s *stderrExecutor) Exec(_ context.Context, request exec.Request) (exec.Result, error) {
	s.requests = append(s.requests, request)
	if strings.Contains(string(request.Stdin), "CREATE DATABASE") || strings.Contains(string(request.Stdin), "CREATE ROLE \"") && strings.Contains(string(request.Stdin), "LOGIN NOSUPERUSER") {
		return exec.Result{ExitCode: 3, Stderr: []byte(s.stderr)}, nil
	}
	if strings.Contains(string(request.Stdin), "FROM pg_database") {
		return exec.Result{Stdout: []byte("app\n")}, nil
	}
	return exec.Result{}, nil
}

func realStderrFactory(executor exec.Executor, db *v1alpha1.Database) EngineFactory {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: PlatformNamespace, Labels: map[string]string{"cnpg.io/cluster": string(db.UID), "cnpg.io/instanceRole": "primary"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	runner := exec.Runner{Pods: fake.NewClientBuilder().WithObjects(pod).Build(), Executor: executor, Locks: &exec.Locks{}, Timeout: 5 * time.Second}
	target := postgres.Target(PlatformNamespace, Name(db.UID))
	return func(context.Context, v1alpha1.Database) (Engine, error) {
		return Engine{Timeout: 2 * time.Minute, Run: func(ctx context.Context, script string) (string, error) {
			return runner.Run(ctx, Name(db.UID), target, script)
		}}, nil
	}
}

func TestSchemaOfAnExistingDatabaseIsAdoptedWithTheRealSQLState(t *testing.T) {
	c := startEnv(t)
	db := readyDatabase(t, c)
	executor := &stderrExecutor{stderr: `ERROR:  42P04: database "shop" already exists` + "\nLOCATION:  createdb, dbcommands.c:500"}
	schema := newSchema(t, c, db, "shop", nil)
	result, err := (&SchemaReconciler{Client: c, Engine: realStderrFactory(executor, db)}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schema)})
	must(t, err)
	reloadSchema(t, c, schema)
	if reason, _ := readyReasonOf(schema.Status.Conditions); reason != ReasonReady || schema.Status.Name != "shop" || result.RequeueAfter == 0 {
		t.Fatalf("status %+v result %+v", schema.Status, result)
	}
	groups := 0
	for _, request := range executor.requests {
		if strings.Contains(string(request.Stdin), `CREATE ROLE "shop_ro" NOLOGIN`) && !strings.Contains(string(request.Stdin), "CREATE DATABASE") {
			groups++
		}
	}
	if groups != 1 {
		t.Fatalf("the group roles were not ensured after the adopt: %d", groups)
	}
}

func TestUserWithAnExistingRoleIsAdoptedWithTheRealSQLState(t *testing.T) {
	c := startEnv(t)
	db := readyDatabase(t, c)
	executor := &stderrExecutor{stderr: `ERROR:  42710: role "alice" already exists`}
	user := newUser(t, c, db, "alice", nil)
	_, err := (&UserReconciler{Client: c, Engine: realStderrFactory(executor, db)}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(user)})
	must(t, err)
	if reason, _ := readyReasonOf(reloadUser(t, c, user).Status.Conditions); reason != ReasonReady || user.Status.AppliedGrants == "" {
		t.Fatalf("status %+v", user.Status)
	}
}
