package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func newRecipient(t *testing.T) *age.X25519Identity {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func captureLog() (func(string), func() []string) {
	var lines []string
	return func(line string) { lines = append(lines, line) }, func() []string { return lines }
}

func TestArchiveRecipientsWithoutSettingIsHostOnly(t *testing.T) {
	host := newRecipient(t).Recipient()
	log, lines := captureLog()
	got, err := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != host || len(lines()) != 0 {
		t.Fatalf("recipients %v, log %v", got, lines())
	}
}

func TestArchiveRecipientsAddsAdminKeysAfterHost(t *testing.T) {
	host := newRecipient(t).Recipient()
	first, second := newRecipient(t).Recipient(), newRecipient(t).Recipient()
	createSetting(t, archiveRecipientsKey, first.String()+"\n"+second.String())
	log, lines := captureLog()
	got, err := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != host || got[1].(*age.X25519Recipient).String() != first.String() || got[2].(*age.X25519Recipient).String() != second.String() || len(lines()) != 0 {
		t.Fatalf("recipients %v, log %v", got, lines())
	}
}

func TestArchiveRecipientsInvalidSettingFallsBackToHost(t *testing.T) {
	host := newRecipient(t).Recipient()
	createSetting(t, archiveRecipientsKey, "age1bad")
	log, lines := captureLog()
	got, err := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != host {
		t.Fatalf("recipients %v", got)
	}
	logged := lines()
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "backup.archive-recipients is invalid, sealing etcd-1.tar.gz.age to the host key only: ") || !strings.Contains(logged[0], "age1bad") {
		t.Fatalf("log %v", logged)
	}
}

var settingReadFailures = map[string]error{
	"forbidden": apierrors.NewForbidden(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "settings"}, archiveRecipientsKey, errors.New("denied")),
	"timeout":   apierrors.NewTimeoutError("the server was too slow", 1),
	"plain":     errors.New("connection refused"),
}

func clientFailingOnRecipients(t *testing.T, failure error) client.Client {
	t.Helper()
	inner, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	return interceptor.NewClient(inner, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == archiveRecipientsKey {
				return failure
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

func TestArchiveRecipientsFailsWhenTheSettingCannotBeRead(t *testing.T) {
	host := newRecipient(t).Recipient()
	for name, failure := range settingReadFailures {
		t.Run(name, func(t *testing.T) {
			log, lines := captureLog()
			got, err := archiveRecipients(context.Background(), clientFailingOnRecipients(t, failure), host, "etcd-1.tar.gz.age", log)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), archiveRecipientsKey) {
				t.Fatalf("error %v, want %v", err, failure)
			}
			if got != nil || len(lines()) != 0 {
				t.Fatalf("recipients %v, log %v", got, lines())
			}
		})
	}
}

func TestArchiveRecipientsInvalidValueStillSealsToTheHost(t *testing.T) {
	host := newRecipient(t).Recipient()
	createSetting(t, archiveRecipientsKey, "ssh-ed25519 AAAA")
	log, lines := captureLog()
	got, err := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if err != nil || len(got) != 1 || got[0] != host || len(lines()) != 1 {
		t.Fatalf("recipients %v, error %v, log %v", got, err, lines())
	}
}

func TestBackupFailsWhenTheRecipientsCannotBeRead(t *testing.T) {
	for name, failure := range settingReadFailures {
		t.Run(name, func(t *testing.T) {
			exec := &fakeK0sBackup{}
			env := backupEnv(t, exec)
			env.Client = clientFailingOnRecipients(t, failure)
			if _, err := backup(context.Background(), env); !errors.Is(err, failure) {
				t.Fatalf("error %v, want %v", err, failure)
			}
			if entries, _ := os.ReadDir(filepath.Join(env.Deps.Root, backupDir)); len(entries) != 0 {
				t.Fatalf("a backup without recipients must leave no file: %v", entries)
			}
			if len(exec.calls) != 0 {
				t.Fatalf("k0s calls %v", exec.calls)
			}
		})
	}
}

func TestEtcdSnapshotBacksOffAndRetriesWhenTheRecipientsCannotBeRead(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	exec := &fakeK0sBackup{}
	deps := snapshotDeps(t, exec, at("2026-10-08T02:30:00Z"))
	failing := clientFailingOnRecipients(t, settingReadFailures["forbidden"])
	if err := RunEtcdSnapshots(context.Background(), failing, deps); !errors.Is(err, settingReadFailures["forbidden"]) {
		t.Fatalf("error %v", err)
	}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, []string{snapshotFailureFile}) {
		t.Fatalf("files %v", got)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("k0s calls %v", exec.calls)
	}
	deps.Now = func() time.Time { return at("2026-10-08T02:32:00Z") }
	if err := RunEtcdSnapshots(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, deps, "*"); !slices.Equal(got, []string{"etcd-20261008T023200Z.tar.gz.age"}) {
		t.Fatalf("files %v", got)
	}
}
