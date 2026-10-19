package operator

import (
	"crypto/rand"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

var rotationNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type configChain struct {
	one, added, promoted, removed []byte
}

func newConfigChain(t *testing.T) configChain {
	t.Helper()
	one, err := encryption.NewConfig(rand.Reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, err := encryption.AddKey(one, "key2", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := encryption.Promote(added, "key2")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := encryption.RemoveKey(promoted, "key1")
	if err != nil {
		t.Fatal(err)
	}
	return configChain{one: one, added: added, promoted: promoted, removed: removed}
}

func controlPlaneHost(name, hash string) v1alpha1.Host {
	return v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}},
		Status: v1alpha1.HostStatus{
			Checks:     &v1alpha1.HostChecks{EtcdHealthy: true},
			Encryption: &v1alpha1.EncryptionFileStatus{Hash: hash},
		},
	}
}

func withGrant(host v1alpha1.Host, hash string) v1alpha1.Host {
	host.Annotations = map[string]string{v1alpha1.AnnotationEncryptionApply: hash}
	return host
}

func phaseStatus(phase string) v1alpha1.EncryptionRotationStatus {
	return v1alpha1.EncryptionRotationStatus{Phase: phase, OldKey: "key1", NewKey: "key2"}
}

func keysOf(t *testing.T, content []byte) []string {
	t.Helper()
	names, err := encryption.Keys(content)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRotationStartsOnRotate(t *testing.T) {
	chain := newConfigChain(t)
	got := nextRotation(rotationInput{Action: v1alpha1.EncryptionActionRotate, Secret: chain.one, Hosts: []v1alpha1.Host{controlPlaneHost("a", encryption.Hash(chain.one))}, Now: rotationNow, Random: rand.Reader})
	if got.Status.Phase != v1alpha1.RotationAddingKey || got.Status.OldKey != "key1" || got.Status.NewKey != "key2" {
		t.Fatalf("status %+v", got.Status)
	}
	if !got.ClearAction || got.Grant != "" {
		t.Fatalf("decision %+v", got)
	}
	if !slices.Equal(keysOf(t, got.Secret), []string{"key1", "key2"}) {
		t.Fatalf("secret keys %v", keysOf(t, got.Secret))
	}
}

func TestRotationRefusals(t *testing.T) {
	chain := newConfigChain(t)
	cases := []struct {
		name    string
		in      rotationInput
		message string
	}{
		{"kms", rotationInput{Action: v1alpha1.EncryptionActionRotate, KMS: true, Secret: chain.one}, "KMS"},
		{"upgrade", rotationInput{Action: v1alpha1.EncryptionActionRotate, Upgrading: true, Secret: chain.one}, "upgrade"},
		{"no secret", rotationInput{Action: v1alpha1.EncryptionActionRotate}, "not encrypted"},
		{"in progress", rotationInput{Action: v1alpha1.EncryptionActionRotate, Secret: chain.added, Status: phaseStatus(v1alpha1.RotationAddingKey)}, "in progress"},
		{"two keys already", rotationInput{Action: v1alpha1.EncryptionActionRotate, Secret: chain.added}, "unfinished"},
		{"revert while idle", rotationInput{Action: v1alpha1.EncryptionActionRevert, Secret: chain.one}, "no rotation"},
		{"revert too late", rotationInput{Action: v1alpha1.EncryptionActionRevert, Secret: chain.promoted, Status: phaseStatus(v1alpha1.RotationRemovingKey)}, "already removed"},
	}
	for _, c := range cases {
		c.in.Random = rand.Reader
		got := nextRotation(c.in)
		if !got.ClearAction || got.Secret != nil || got.Grant != "" || got.Rewrite {
			t.Errorf("%s: decision %+v", c.name, got)
		}
		if got.Status.Phase != c.in.Status.Phase || !strings.Contains(got.Status.Message, c.message) {
			t.Errorf("%s: status %+v", c.name, got.Status)
		}
	}
}

func TestRotationWithoutActionAndIdleChangesNothing(t *testing.T) {
	chain := newConfigChain(t)
	got := nextRotation(rotationInput{Secret: chain.one, Hosts: []v1alpha1.Host{controlPlaneHost("a", "x")}, Now: rotationNow})
	if got.Status.Phase != "" || got.ClearAction || got.Secret != nil || got.Grant != "" {
		t.Fatalf("decision %+v", got)
	}
}

func TestAddingKeyGrantsTheFirstHostByName(t *testing.T) {
	chain := newConfigChain(t)
	old := encryption.Hash(chain.one)
	hosts := []v1alpha1.Host{controlPlaneHost("b", old), controlPlaneHost("a", old)}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "a" || got.GrantHash != encryption.Hash(chain.added) || got.Secret != nil {
		t.Fatalf("decision %+v", got)
	}
	if got.Status.Phase != v1alpha1.RotationAddingKey {
		t.Fatalf("phase %q", got.Status.Phase)
	}
}

func TestAddingKeyWritesTheSecretBeforeAnyGrant(t *testing.T) {
	chain := newConfigChain(t)
	hosts := []v1alpha1.Host{controlPlaneHost("a", encryption.Hash(chain.one))}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.one, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !slices.Equal(keysOf(t, got.Secret), []string{"key1", "key2"}) {
		t.Fatalf("decision %+v", got)
	}
}

func TestOneGrantAtATime(t *testing.T) {
	chain := newConfigChain(t)
	old, want := encryption.Hash(chain.one), encryption.Hash(chain.added)
	hosts := []v1alpha1.Host{withGrant(controlPlaneHost("a", old), want), controlPlaneHost("b", old)}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !strings.Contains(got.Status.Message, "a") {
		t.Fatalf("decision %+v", got)
	}
}

func TestARestartPendingHostIsNotConverged(t *testing.T) {
	chain := newConfigChain(t)
	want := encryption.Hash(chain.added)
	pending := withGrant(controlPlaneHost("a", want), want)
	pending.Status.Encryption.RestartPending = true
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: []v1alpha1.Host{pending}, Now: rotationNow, Random: rand.Reader})
	if got.Status.Phase != v1alpha1.RotationAddingKey || got.Grant != "" {
		t.Fatalf("decision %+v", got)
	}
}

func TestNoGrantWhileEtcdIsUnhealthy(t *testing.T) {
	chain := newConfigChain(t)
	old, want := encryption.Hash(chain.one), encryption.Hash(chain.added)
	sick := controlPlaneHost("c", want)
	sick.Status.Checks.EtcdHealthy = false
	hosts := []v1alpha1.Host{controlPlaneHost("a", want), controlPlaneHost("b", old), sick}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !strings.Contains(got.Status.Message, "etcd") {
		t.Fatalf("decision %+v", got)
	}
}

func TestAFailedHostStallsTheRotation(t *testing.T) {
	chain := newConfigChain(t)
	old, want := encryption.Hash(chain.one), encryption.Hash(chain.added)
	failed := withGrant(controlPlaneHost("a", old), want)
	failed.Status.Encryption.FailedHash = want
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: []v1alpha1.Host{failed, controlPlaneHost("b", old)}, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !strings.Contains(got.Status.Message, "failed") {
		t.Fatalf("decision %+v", got)
	}
}

func TestWorkerHostsDoNotCount(t *testing.T) {
	chain := newConfigChain(t)
	want := encryption.Hash(chain.added)
	worker := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "w"}, Spec: v1alpha1.HostSpec{Roles: []string{"workload"}}}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: []v1alpha1.Host{worker, controlPlaneHost("a", want)}, Now: rotationNow, Random: rand.Reader})
	if got.Status.Phase != v1alpha1.RotationPromotingKey {
		t.Fatalf("phase %q", got.Status.Phase)
	}
}

func TestPhasesAdvanceOnlyWhenEveryHostConverged(t *testing.T) {
	chain := newConfigChain(t)
	cases := []struct {
		name   string
		status v1alpha1.EncryptionRotationStatus
		secret []byte
		next   string
	}{
		{"adding to promoting", phaseStatus(v1alpha1.RotationAddingKey), chain.added, v1alpha1.RotationPromotingKey},
		{"adding resumes with the secret one step ahead", phaseStatus(v1alpha1.RotationAddingKey), chain.promoted, v1alpha1.RotationPromotingKey},
		{"promoting to rewriting", phaseStatus(v1alpha1.RotationPromotingKey), chain.promoted, v1alpha1.RotationRewritingSecrets},
	}
	for _, c := range cases {
		hosts := []v1alpha1.Host{controlPlaneHost("a", encryption.Hash(c.secret)), controlPlaneHost("b", encryption.Hash(c.secret))}
		got := nextRotation(rotationInput{Status: c.status, Secret: c.secret, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
		if got.Status.Phase != c.next || got.Secret != nil || got.Grant != "" {
			t.Errorf("%s: decision %+v", c.name, got)
		}
	}
}

func TestPromotingWritesTheSecretFirst(t *testing.T) {
	chain := newConfigChain(t)
	hosts := []v1alpha1.Host{controlPlaneHost("a", encryption.Hash(chain.added))}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationPromotingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !slices.Equal(keysOf(t, got.Secret), []string{"key2", "key1"}) {
		t.Fatalf("decision %+v", got)
	}
}

func TestRewritingRunsOnlyOnConvergedHosts(t *testing.T) {
	chain := newConfigChain(t)
	want := encryption.Hash(chain.promoted)
	ready := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationRewritingSecrets), Secret: chain.promoted, Hosts: []v1alpha1.Host{controlPlaneHost("a", want)}, Now: rotationNow, Random: rand.Reader})
	if !ready.Rewrite || ready.Status.Phase != v1alpha1.RotationRewritingSecrets {
		t.Fatalf("decision %+v", ready)
	}
	behind := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationRewritingSecrets), Secret: chain.promoted, Hosts: []v1alpha1.Host{controlPlaneHost("a", "other")}, Now: rotationNow, Random: rand.Reader})
	if behind.Rewrite || behind.Grant != "a" {
		t.Fatalf("decision %+v", behind)
	}
}

func TestFinishRewriteMovesToRemovingKey(t *testing.T) {
	got := finishRewrite(phaseStatus(v1alpha1.RotationRewritingSecrets), 12)
	if got.Phase != v1alpha1.RotationRemovingKey || got.RewrittenSecrets != 12 {
		t.Fatalf("status %+v", got)
	}
}

func TestRemovingKeyDropsTheOldKeyThenEnds(t *testing.T) {
	chain := newConfigChain(t)
	status := phaseStatus(v1alpha1.RotationRemovingKey)
	write := nextRotation(rotationInput{Status: status, Secret: chain.promoted, Hosts: []v1alpha1.Host{controlPlaneHost("a", encryption.Hash(chain.promoted))}, Now: rotationNow, Random: rand.Reader})
	if write.Grant != "" || !slices.Equal(keysOf(t, write.Secret), []string{"key2"}) {
		t.Fatalf("decision %+v", write)
	}
	done := encryption.Hash(chain.removed)
	hosts := []v1alpha1.Host{withGrant(controlPlaneHost("a", done), done), controlPlaneHost("b", done)}
	end := nextRotation(rotationInput{Status: status, Secret: chain.removed, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if end.Status.Phase != "" || end.Status.LastRotatedAt == nil || !end.Status.LastRotatedAt.Time.Equal(rotationNow) {
		t.Fatalf("status %+v", end.Status)
	}
	if !slices.Equal(end.Revoke, []string{"a"}) {
		t.Fatalf("revoke %v", end.Revoke)
	}
}

func TestStaleGrantsAreRevokedAndTheCurrentOneStays(t *testing.T) {
	chain := newConfigChain(t)
	want := encryption.Hash(chain.added)
	hosts := []v1alpha1.Host{withGrant(controlPlaneHost("a", want), want), withHandled(withGrant(controlPlaneHost("b", want), "stale"), "stale")}
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if !slices.Equal(got.Revoke, []string{"b"}) || got.Grant != "" || got.Status.Phase != v1alpha1.RotationAddingKey {
		t.Fatalf("decision %+v", got)
	}
}

func TestRevertInAddingKeyRemovesTheNewKey(t *testing.T) {
	chain := newConfigChain(t)
	got := nextRotation(rotationInput{Action: v1alpha1.EncryptionActionRevert, Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Now: rotationNow, Random: rand.Reader})
	want := v1alpha1.EncryptionRotationStatus{Phase: v1alpha1.RotationRemovingKey, OldKey: "key2", NewKey: "key1", Reverting: true}
	if got.Status.Phase != want.Phase || got.Status.OldKey != want.OldKey || got.Status.NewKey != want.NewKey || !got.Status.Reverting || !got.ClearAction {
		t.Fatalf("decision %+v", got)
	}
	if !slices.Equal(keysOf(t, got.Secret), []string{"key1"}) {
		t.Fatalf("secret keys %v", keysOf(t, got.Secret))
	}
}

func TestRevertAfterPromoteSwapsTheRoles(t *testing.T) {
	chain := newConfigChain(t)
	for _, phase := range []string{v1alpha1.RotationPromotingKey, v1alpha1.RotationRewritingSecrets} {
		got := nextRotation(rotationInput{Action: v1alpha1.EncryptionActionRevert, Status: phaseStatus(phase), Secret: chain.promoted, Now: rotationNow, Random: rand.Reader})
		if got.Status.Phase != v1alpha1.RotationPromotingKey || got.Status.OldKey != "key2" || got.Status.NewKey != "key1" || !got.Status.Reverting {
			t.Fatalf("%s: status %+v", phase, got.Status)
		}
		if !slices.Equal(keysOf(t, got.Secret), []string{"key1", "key2"}) {
			t.Fatalf("%s: secret keys %v", phase, keysOf(t, got.Secret))
		}
	}
}

func TestSecondRevertIsIgnored(t *testing.T) {
	chain := newConfigChain(t)
	status := phaseStatus(v1alpha1.RotationPromotingKey)
	status.Reverting = true
	got := nextRotation(rotationInput{Action: v1alpha1.EncryptionActionRevert, Status: status, Secret: chain.added, Now: rotationNow, Random: rand.Reader})
	if got.Status.OldKey != "key1" || !got.ClearAction || !strings.Contains(got.Status.Message, "already") {
		t.Fatalf("decision %+v", got)
	}
}

func TestAnUpgradePausesARunningRotation(t *testing.T) {
	chain := newConfigChain(t)
	got := nextRotation(rotationInput{Upgrading: true, Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: []v1alpha1.Host{controlPlaneHost("a", "old")}, Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || got.Secret != nil || got.Rewrite || !strings.Contains(got.Status.Message, "upgrade") || got.Status.Phase != v1alpha1.RotationAddingKey {
		t.Fatalf("decision %+v", got)
	}
}

func TestAMissingSecretStallsARunningRotation(t *testing.T) {
	got := nextRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Now: rotationNow, Random: rand.Reader})
	if got.Secret != nil || got.Grant != "" || !strings.Contains(got.Status.Message, "missing") {
		t.Fatalf("decision %+v", got)
	}
}
