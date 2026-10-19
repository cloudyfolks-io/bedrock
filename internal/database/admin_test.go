package database

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
)

type scripts struct {
	mu   sync.Mutex
	runs []string
}

func (s *scripts) run(_ context.Context, script string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, script)
	return "", nil
}

func (s *scripts) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

const execTimeout = 120 * time.Second

func TestBootstrapRunsOnceAndIsRecorded(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, _ := storedDatabase(t, c, nil)
	recorder := &scripts{}
	must(t, EnsureBootstrapped(ctx, c, recorder.run, execTimeout, db))
	must(t, EnsureBootstrapped(ctx, c, recorder.run, execTimeout, db))
	var fresh v1alpha1.Database
	must(t, c.Get(ctx, client.ObjectKeyFromObject(db), &fresh))
	if recorder.count() != 1 || !Bootstrapped(fresh) {
		t.Fatalf("runs %d bootstrapped %v", recorder.count(), Bootstrapped(fresh))
	}
	want, _ := postgres.BootstrapScript(execTimeout)
	if recorder.runs[0] != want {
		t.Fatal("the bootstrap script differs")
	}
	must(t, EnsureBootstrapped(ctx, c, recorder.run, execTimeout, &fresh))
	if recorder.count() != 1 {
		t.Fatal("a reload ran the bootstrap again")
	}
}

func TestSyncAdminOfAFreshDatabaseRunsNothing(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	recorder := &scripts{}
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	if recorder.count() != 0 {
		t.Fatalf("runs %d", recorder.count())
	}
}

func TestSyncAdminAfterARestoreResetsThePasswordOnce(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, func(db *v1alpha1.Database) { db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "old"} })
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	state, err := ReadAdmin(ctx, c, *db)
	must(t, err)
	recorder := &scripts{}
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	if recorder.count() != 1 || strings.Contains(recorder.runs[0], state.Password) || !strings.Contains(recorder.runs[0], `ALTER ROLE "dbadmin" PASSWORD 'SCRAM-SHA-256$`) {
		t.Fatalf("runs %v", recorder.runs)
	}
}

func TestSyncAdminResetsOnAChangeOfTheAnnotationOnly(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, func(db *v1alpha1.Database) {
		db.Annotations = map[string]string{v1alpha1.AnnotationResetPassword: "first"}
	})
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	recorder := &scripts{}
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	if recorder.count() != 0 {
		t.Fatal("the annotation present at create time caused a reset")
	}
	before, _ := ReadAdmin(ctx, c, *db)

	db.Annotations[v1alpha1.AnnotationResetPassword] = "second"
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	after, _ := ReadAdmin(ctx, c, *db)
	if recorder.count() != 1 || after.Password == before.Password || !after.Synced || after.Applied != "second" {
		t.Fatalf("runs %d before %+v after %+v", recorder.count(), before, after)
	}
	must(t, SyncAdmin(ctx, c, recorder.run, execTimeout, *db))
	if recorder.count() != 1 {
		t.Fatal("the same token reset the password twice")
	}
}

type failingOnce struct {
	calls int
}

func (f *failingOnce) run(context.Context, string) (string, error) {
	f.calls++
	if f.calls == 1 {
		return "", exec.ErrNotReady
	}
	return "", nil
}

func TestSyncAdminRetriesTheEngineStepAfterAFailure(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, func(db *v1alpha1.Database) { db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "old"} })
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	runner := &failingOnce{}
	if err := SyncAdmin(ctx, c, runner.run, execTimeout, *db); err == nil {
		t.Fatal("the failure was swallowed")
	}
	state, _ := ReadAdmin(ctx, c, *db)
	if state.Synced {
		t.Fatal("marked synced after a failed step")
	}
	must(t, SyncAdmin(ctx, c, runner.run, execTimeout, *db))
	state, _ = ReadAdmin(ctx, c, *db)
	if !state.Synced || runner.calls != 2 {
		t.Fatalf("state %+v calls %d", state, runner.calls)
	}
}

type recordingExecutor struct {
	requests []exec.Request
}

func (r *recordingExecutor) Exec(_ context.Context, request exec.Request) (exec.Result, error) {
	r.requests = append(r.requests, request)
	return exec.Result{}, nil
}

func TestNoPasswordReachesTheCommandLineTheScriptOrTheLog(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, func(db *v1alpha1.Database) { db.Spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "old"} })
	must(t, EnsureEngineSecrets(ctx, c, *db, project))
	state, err := ReadAdmin(ctx, c, *db)
	must(t, err)

	var lines []string
	sink := funcr.New(func(prefix, args string) { lines = append(lines, prefix+" "+args) }, funcr.Options{Verbosity: 5})
	ctx = logf.IntoContext(ctx, sink)
	executor := &recordingExecutor{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "primary", Namespace: PlatformNamespace, Labels: map[string]string{"cnpg.io/cluster": string(db.UID), "cnpg.io/instanceRole": "primary"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	runner := exec.Runner{Pods: fake.NewClientBuilder().WithObjects(pod).Build(), Executor: executor, Locks: &exec.Locks{}, Timeout: 5 * time.Second}
	target := postgres.Target(PlatformNamespace, string(db.UID))
	run := func(ctx context.Context, script string) (string, error) {
		return runner.Run(ctx, string(db.UID), target, script)
	}
	must(t, EnsureBootstrapped(ctx, c, run, execTimeout, db))
	must(t, SyncAdmin(ctx, c, run, execTimeout, *db))

	if len(executor.requests) != 2 {
		t.Fatalf("requests %d", len(executor.requests))
	}
	for _, request := range executor.requests {
		if strings.Contains(strings.Join(request.Command, " "), state.Password) || strings.Contains(string(request.Stdin), state.Password) {
			t.Fatal("the password reached the command line or the SQL text")
		}
	}
	for _, line := range lines {
		if strings.Contains(line, state.Password) {
			t.Fatalf("log line %q holds the password", line)
		}
	}
}
