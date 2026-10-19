package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeLister struct {
	directories []string
	err         error
	bucket      string
	prefix      string
}

func (l *fakeLister) ListDirectories(_ context.Context, bucket, prefix string) ([]string, error) {
	l.bucket, l.prefix = bucket, prefix
	return l.directories, l.err
}

func backupObject(state, destination, completed string, created time.Time) unstructured.Unstructured {
	status := map[string]any{"state": state, "destination": destination}
	if completed != "" {
		status["completed"] = completed
	}
	obj := unstructured.Unstructured{Object: map[string]any{"status": status}}
	obj.SetCreationTimestamp(metav1.NewTime(created))
	return obj
}

func TestNewestDestinationPicksTheNewestSucceededBackup(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	backups := []unstructured.Unstructured{
		backupObject("Succeeded", "s3://b/u/old-full", "2026-10-01T02:00:00Z", base),
		backupObject("Succeeded", "s3://b/u/new-full", "2026-10-03T02:00:00Z", base),
		backupObject("Failed", "s3://b/u/failed-full", "2026-10-04T02:00:00Z", base),
		backupObject("Succeeded", "", "2026-10-05T02:00:00Z", base),
		backupObject("Running", "s3://b/u/running-full", "", base),
	}
	got, ok := NewestDestination(backups)
	if !ok || got != "s3://b/u/new-full" {
		t.Fatalf("destination %q %v", got, ok)
	}
}

func TestNewestDestinationFallsBackToTheCreationTime(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	backups := []unstructured.Unstructured{
		backupObject("Succeeded", "s3://b/u/a-full", "", base),
		backupObject("Succeeded", "s3://b/u/b-full", "", base.Add(time.Hour)),
	}
	if got, _ := NewestDestination(backups); got != "s3://b/u/b-full" {
		t.Fatalf("destination %q", got)
	}
}

func TestNewestDestinationWithoutBackups(t *testing.T) {
	if got, ok := NewestDestination(nil); ok || got != "" {
		t.Fatalf("destination %q %v", got, ok)
	}
	failed := []unstructured.Unstructured{backupObject("Failed", "s3://b/u/x-full", "", time.Now())}
	if _, ok := NewestDestination(failed); ok {
		t.Fatal("a failed backup is a destination")
	}
}

func TestDiscoverDestinationReadsTheNewestDirectoryFromTheList(t *testing.T) {
	lister := &fakeLister{directories: []string{
		sourceUID + "/2026-10-01-02:00:00-full/",
		sourceUID + "/2026-10-03-02:00:00-full/",
		sourceUID + "/2026-10-02-02:00:00-full/",
		sourceUID + "/lost+found/",
		sourceUID + "/2026-10-04-02:00:00-incremental/",
	}}
	got, err := DiscoverDestination(context.Background(), lister, "bedrock-database-backups", sourceUID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3://bedrock-database-backups/"+sourceUID+"/2026-10-03-02:00:00-full" {
		t.Fatalf("destination %q", got)
	}
	if lister.bucket != "bedrock-database-backups" || lister.prefix != sourceUID+"/" {
		t.Fatalf("listed %q %q", lister.bucket, lister.prefix)
	}
}

func TestDiscoverDestinationAcceptsClusterPrefixedDirectories(t *testing.T) {
	lister := &fakeLister{directories: []string{"orders-2026-10-02-02:00:00-full", "orders-2026-10-01-02:00:00-full"}}
	got, err := DiscoverDestination(context.Background(), lister, "b", sourceUID)
	if err != nil || got != "s3://b/"+sourceUID+"/orders-2026-10-02-02:00:00-full" {
		t.Fatalf("destination %q %v", got, err)
	}
}

func TestDiscoverDestinationReportsAnEmptyPrefixAndListErrors(t *testing.T) {
	_, err := DiscoverDestination(context.Background(), &fakeLister{directories: []string{"x/"}}, "b", sourceUID)
	if !errors.Is(err, ErrNoBackup) {
		t.Fatalf("empty prefix: %v", err)
	}
	boom := errors.New("boom")
	_, err = DiscoverDestination(context.Background(), &fakeLister{err: boom}, "b", sourceUID)
	if !errors.Is(err, boom) {
		t.Fatalf("list error: %v", err)
	}
}

func TestResolveDestinationPrefersLiveBackupsAndListsOtherwise(t *testing.T) {
	live := []unstructured.Unstructured{backupObject("Succeeded", "s3://b/u/live-full", "2026-10-01T02:00:00Z", time.Now())}
	lister := &fakeLister{directories: []string{"2026-10-09-02:00:00-full"}}
	got, err := ResolveDestination(context.Background(), live, lister, "b", sourceUID)
	if err != nil || got != "s3://b/u/live-full" || lister.prefix != "" {
		t.Fatalf("live: %q %v (listed %q)", got, err, lister.prefix)
	}
	got, err = ResolveDestination(context.Background(), nil, lister, "b", sourceUID)
	if err != nil || got != "s3://b/"+sourceUID+"/2026-10-09-02:00:00-full" {
		t.Fatalf("listed: %q %v", got, err)
	}
}
