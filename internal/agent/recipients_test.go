package agent

import (
	"context"
	"strings"
	"testing"

	"filippo.io/age"
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
	got := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if len(got) != 1 || got[0] != host || len(lines()) != 0 {
		t.Fatalf("recipients %v, log %v", got, lines())
	}
}

func TestArchiveRecipientsAddsAdminKeysAfterHost(t *testing.T) {
	host := newRecipient(t).Recipient()
	first, second := newRecipient(t).Recipient(), newRecipient(t).Recipient()
	createSetting(t, archiveRecipientsKey, first.String()+"\n"+second.String())
	log, lines := captureLog()
	got := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if len(got) != 3 || got[0] != host || got[1].(*age.X25519Recipient).String() != first.String() || got[2].(*age.X25519Recipient).String() != second.String() || len(lines()) != 0 {
		t.Fatalf("recipients %v, log %v", got, lines())
	}
}

func TestArchiveRecipientsInvalidSettingFallsBackToHost(t *testing.T) {
	host := newRecipient(t).Recipient()
	createSetting(t, archiveRecipientsKey, "age1bad")
	log, lines := captureLog()
	got := archiveRecipients(context.Background(), k8sClient, host, "etcd-1.tar.gz.age", log)
	if len(got) != 1 || got[0] != host {
		t.Fatalf("recipients %v", got)
	}
	logged := lines()
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "backup.archive-recipients is invalid, sealing etcd-1.tar.gz.age to the host key only: ") || !strings.Contains(logged[0], "age1bad") {
		t.Fatalf("log %v", logged)
	}
}
