package database

import (
	"context"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func testProject(namespace string) Project {
	return Project{Namespace: namespace, Tenant: "acme", Name: "web"}
}

func storedDatabase(t *testing.T, c client.Client, edit func(*v1alpha1.Database)) (*v1alpha1.Database, Project) {
	t.Helper()
	ensurePlatformNamespace(t, c)
	namespace := projectNamespace(t, c)
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "orders-" + nextSequence(), Namespace: namespace}, Spec: newSpec()}
	if edit != nil {
		edit(db)
	}
	create(t, c, db)
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), db))
	return db, testProject(namespace)
}

func getSecret(t *testing.T, c client.Client, namespace, name string) corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &secret))
	return secret
}

func TestEnsureEngineSecretsIsIdempotentAndKeepsThePasswords(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	superuser := getSecret(t, c, PlatformNamespace, Suffixed(db.UID, SecretSuperuser))
	app := getSecret(t, c, PlatformNamespace, Suffixed(db.UID, SecretApp))
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	again := getSecret(t, c, PlatformNamespace, Suffixed(db.UID, SecretApp))

	if string(superuser.Data["username"]) != "postgres" || len(superuser.Data["password"]) != 32 || superuser.Type != corev1.SecretTypeBasicAuth {
		t.Fatalf("superuser secret %v", superuser.Data)
	}
	if string(app.Data["username"]) != "dbadmin" || len(app.Data["password"]) != 32 || string(app.Data["password"]) == string(superuser.Data["password"]) {
		t.Fatalf("app secret %v", app.Data)
	}
	if string(again.Data["password"]) != string(app.Data["password"]) {
		t.Fatal("a second ensure changed the password")
	}
	if app.Labels[v1alpha1.LabelKind] != Kind || app.Labels[LabelInstance] != string(db.UID) || app.Annotations[platform.AnnotationOwnerUID] != string(db.UID) {
		t.Fatalf("labels %v annotations %v", app.Labels, app.Annotations)
	}
	if len(app.OwnerReferences) != 0 {
		t.Fatal("a platform secret must not have an owner reference")
	}
	if app.Annotations[annotationPasswordSynced] != "true" {
		t.Fatalf("a fresh database starts synced: %v", app.Annotations)
	}
}

func TestEnsureEngineSecretsOfARestoreStartUnsynced(t *testing.T) {
	c := startEnv(t)
	db, project := storedDatabase(t, c, func(db *v1alpha1.Database) { db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "old"} })
	must(t, EnsureEngineSecrets(context.Background(), c, *db, project))
	state, err := ReadAdmin(context.Background(), c, *db)
	must(t, err)
	if state.Synced || len(state.Password) != 32 {
		t.Fatalf("state %+v", state)
	}
}

func TestResetAdminPasswordChangesThePasswordAndMarksItUnsynced(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	before, err := ReadAdmin(ctx, c, *db)
	must(t, err)
	password, err := ResetAdminPassword(ctx, c, *db, "token-1")
	must(t, err)
	after, err := ReadAdmin(ctx, c, *db)
	must(t, err)
	if password == before.Password || after.Password != password || after.Synced || after.Applied != "token-1" {
		t.Fatalf("before %+v after %+v new %s", before, after, password)
	}
	must(t, MarkAdminSynced(ctx, c, *db))
	after, _ = ReadAdmin(ctx, c, *db)
	if !after.Synced {
		t.Fatal("not marked synced")
	}
}

func TestAdminCopyHasTheConnectionFieldsAndOverwritesDrift(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	state, err := ReadAdmin(ctx, c, *db)
	must(t, err)
	must(t, EnsureAdminCopy(ctx, c, *db, project, "10.100.0.7", 5432))

	copySecret := getSecret(t, c, db.Namespace, db.Name+"-admin")
	data := map[string]string{}
	for key, value := range copySecret.Data {
		data[key] = string(value)
	}
	parsed, err := url.Parse(data["uri"])
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if data["username"] != "dbadmin" || data["password"] != state.Password || data["host"] != "10.100.0.7" || data["port"] != "5432" || data["database"] != "app" ||
		parsed.Scheme != "postgresql" || parsed.User.Username() != "dbadmin" || password != state.Password || parsed.Host != "10.100.0.7:5432" || parsed.Path != "/app" {
		t.Fatalf("copy %v", data)
	}
	if len(copySecret.OwnerReferences) != 1 || copySecret.OwnerReferences[0].UID != db.UID || copySecret.Labels[v1alpha1.LabelProject] != "web" {
		t.Fatalf("owner %v labels %v", copySecret.OwnerReferences, copySecret.Labels)
	}

	copySecret.Data["password"] = []byte("tampered")
	copySecret.Data["host"] = []byte("1.2.3.4")
	must(t, c.Update(ctx, &copySecret))
	must(t, EnsureAdminCopy(ctx, c, *db, project, "10.100.0.7", 5432))
	fixed := getSecret(t, c, db.Namespace, db.Name+"-admin")
	if string(fixed.Data["password"]) != state.Password || string(fixed.Data["host"]) != "10.100.0.7" {
		t.Fatalf("drift stayed: %v", fixed.Data)
	}

	must(t, EnsureAdminCopy(ctx, c, *db, project, "10.100.0.8", 5433))
	moved := getSecret(t, c, db.Namespace, db.Name+"-admin")
	if string(moved.Data["host"]) != "10.100.0.8" || !strings.HasSuffix(string(moved.Data["uri"]), "@10.100.0.8:5433/app") {
		t.Fatalf("address change %v", moved.Data)
	}
}

func TestAdminURIEscapesUnusualCharacters(t *testing.T) {
	uri := AdminURI("p@ss/word:1", "10.0.0.1", 5432)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if password, _ := parsed.User.Password(); password != "p@ss/word:1" || parsed.Host != "10.0.0.1:5432" {
		t.Fatalf("uri %s", uri)
	}
}
