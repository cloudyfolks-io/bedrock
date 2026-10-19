package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

func encryptionReconciler(c client.Client) *ClusterReconciler {
	return &ClusterReconciler{Client: c, APIReader: c, Interval: time.Second}
}

func createEncryptionSecret(t *testing.T, c client.Client, content []byte) {
	t.Helper()
	if err := c.Create(context.Background(), encryption.Secret(content)); err != nil {
		t.Fatal(err)
	}
}

func createControlPlane(t *testing.T, c client.Client, name, hash string, members int32) {
	t.Helper()
	ctx := context.Background()
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	host.Status.Checks = &v1alpha1.HostChecks{EtcdHealthy: true, EtcdMembers: members}
	host.Status.Encryption = &v1alpha1.EncryptionFileStatus{Hash: hash}
	if err := c.Status().Update(ctx, host); err != nil {
		t.Fatal(err)
	}
}

func applyGrants(t *testing.T, c client.Client) []string {
	t.Helper()
	ctx := context.Background()
	content, err := readEncryptionContent(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	var hosts v1alpha1.HostList
	if err := c.List(ctx, &hosts); err != nil {
		t.Fatal(err)
	}
	var applied []string
	for _, host := range hosts.Items {
		grant := host.Annotations[v1alpha1.AnnotationEncryptionApply]
		if grant == "" || grant != encryption.Hash(content) || host.Status.Encryption.Hash == grant {
			continue
		}
		host.Status.Encryption = &v1alpha1.EncryptionFileStatus{Hash: grant}
		if err := c.Status().Update(ctx, &host); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, host.Name)
	}
	return applied
}

func TestReconcileEncryptionRunsAFullRotation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	chain := newConfigChain(t)
	cluster := newCluster("v0.1.0")
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	createValid(t, c, cluster)
	createEncryptionSecret(t, c, chain.one)
	for _, name := range []string{"cp-1", "cp-2"} {
		createControlPlane(t, c, name, encryption.Hash(chain.one), 2)
	}
	for _, name := range []string{"one", "two"} {
		if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}, Data: map[string][]byte{"k": []byte("v")}}); err != nil {
			t.Fatal(err)
		}
	}

	var phases, applied []string
	for range 60 {
		if _, err := encryptionReconciler(c).reconcileEncryption(ctx); err != nil {
			t.Fatal(err)
		}
		current := currentCluster(t, c).Status.Encryption
		if current != nil && current.Phase != "" && (len(phases) == 0 || phases[len(phases)-1] != current.Phase) {
			phases = append(phases, current.Phase)
		}
		applied = append(applied, applyGrants(t, c)...)
		if current != nil && current.Phase == "" && current.LastRotatedAt != nil {
			break
		}
	}
	wantPhases := []string{v1alpha1.RotationAddingKey, v1alpha1.RotationPromotingKey, v1alpha1.RotationRewritingSecrets, v1alpha1.RotationRemovingKey}
	if !slices.Equal(phases, wantPhases) {
		t.Fatalf("phases %v", phases)
	}
	wantApplied := []string{"cp-1", "cp-2", "cp-1", "cp-2", "cp-1", "cp-2"}
	if !slices.Equal(applied, wantApplied) {
		t.Fatalf("hosts applied in this order: %v", applied)
	}
	content, err := readEncryptionContent(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if names, _ := encryption.Keys(content); !slices.Equal(names, []string{"key2"}) {
		t.Fatalf("final keys %v", names)
	}
	final := currentCluster(t, c)
	if final.Spec.Encryption.Action != "" || final.Status.Encryption.RewrittenSecrets != 0 {
		t.Fatalf("action %q status %+v", final.Spec.Encryption.Action, final.Status.Encryption)
	}
	var hosts v1alpha1.HostList
	if err := c.List(ctx, &hosts); err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts.Items {
		if _, held := host.Annotations[v1alpha1.AnnotationEncryptionApply]; held {
			t.Errorf("host %s still holds a grant", host.Name)
		}
	}
}

func TestReconcileEncryptionRefusesKMS(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	chain := newConfigChain(t)
	cluster := newCluster("v0.1.0")
	cluster.Spec.Encryption = kmsEncryption("unix:///run/kms/kms.sock")
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	createValid(t, c, cluster)
	createEncryptionSecret(t, c, chain.one)

	if _, err := encryptionReconciler(c).reconcileEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	got := currentCluster(t, c)
	if got.Spec.Encryption.Action != "" || got.Status.Encryption == nil || !strings.Contains(got.Status.Encryption.Message, "KMS") || got.Status.Encryption.Phase != "" {
		t.Fatalf("spec action %q status %+v", got.Spec.Encryption.Action, got.Status.Encryption)
	}
	content, err := readEncryptionContent(ctx, c)
	if err != nil || string(content) != string(chain.one) {
		t.Fatalf("the Secret must not change: %v", err)
	}
}

func TestReconcileEncryptionDoesNothingWhenIdle(t *testing.T) {
	c, _ := StartTestEnv(t)
	createValid(t, c, newCluster("v0.1.0"))
	wait, err := encryptionReconciler(c).reconcileEncryption(context.Background())
	if err != nil || wait != 0 {
		t.Fatalf("wait %v err %v", wait, err)
	}
	if currentCluster(t, c).Status.Encryption != nil {
		t.Fatal("an idle cluster must keep no encryption status")
	}
}

func TestReconcileEncryptionRevertsInAddingKey(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	chain := newConfigChain(t)
	cluster := newCluster("v0.1.0")
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	createValid(t, c, cluster)
	createEncryptionSecret(t, c, chain.one)
	createControlPlane(t, c, "cp-1", encryption.Hash(chain.one), 1)
	reconciler := encryptionReconciler(c)
	if _, err := reconciler.reconcileEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	current := currentCluster(t, c)
	current.Spec.Encryption.Action = v1alpha1.EncryptionActionRevert
	if err := c.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if _, err := reconciler.reconcileEncryption(ctx); err != nil {
			t.Fatal(err)
		}
		applyGrants(t, c)
		if status := currentCluster(t, c).Status.Encryption; status != nil && status.Phase == "" {
			break
		}
	}
	content, _ := readEncryptionContent(ctx, c)
	if names, _ := encryption.Keys(content); !slices.Equal(names, []string{"key1"}) {
		t.Fatalf("keys after revert %v", names)
	}
	if got := currentCluster(t, c).Status.Encryption; got == nil || !strings.Contains(got.Message, "reverted") {
		t.Fatalf("status %+v", got)
	}
}

func createSecrets(t *testing.T, c client.Client, namespace string, count int) {
	t.Helper()
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	for index := range count {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "s" + string(rune('a'+index))}, Data: map[string][]byte{"k": []byte("v")}}
		if err := c.Create(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRewriteSecretsPagesThroughEverySecret(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createSecrets(t, c, "rewrite-a", 5)
	createSecrets(t, c, "rewrite-b", 2)
	var all corev1.SecretList
	if err := c.List(ctx, &all); err != nil {
		t.Fatal(err)
	}
	writer := newScriptedWriter(c)
	for index := range all.Items {
		writer.moved[all.Items[index].Namespace+"/"+all.Items[index].Name] = true
	}
	result, err := rewriteSecrets(ctx, c, writer, 2)
	if err != nil || result.Updated != int64(len(all.Items)) || result.Updated < 7 {
		t.Fatalf("result %+v of %d err %v", result, len(all.Items), err)
	}
	var after corev1.SecretList
	if err := c.List(ctx, &after); err != nil {
		t.Fatal(err)
	}
	for index := range all.Items {
		if all.Items[index].ResourceVersion != after.Items[index].ResourceVersion {
			t.Errorf("secret %s/%s changed without encryption", all.Items[index].Namespace, all.Items[index].Name)
		}
	}
}

type updateFails struct {
	client.Client
	err error
}

func (u updateFails) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return u.err
}

func TestRewriteSecretsReportsEverySecretThatCannotBeWritten(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createSecrets(t, c, "rewrite-c", 3)
	var all corev1.SecretList
	if err := c.List(ctx, &all); err != nil {
		t.Fatal(err)
	}
	conflict := updateFails{Client: c, err: apierrors.NewConflict(secretsResource, "x", errors.New("changed"))}
	result, err := rewriteSecrets(ctx, c, conflict, 2)
	if err != nil || len(result.Failed) != len(all.Items) || result.Updated != 0 {
		t.Fatalf("a conflict that stays must be reported: %+v err %v", result, err)
	}
	failing := updateFails{Client: c, err: errors.New("forbidden")}
	result, err = rewriteSecrets(ctx, c, failing, 2)
	if err != nil || len(result.Failed) != len(all.Items) {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestOperatorRoleCanRewriteSecrets(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var committed rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &committed); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"list", "update"} {
		if !allows(role, "", "secrets", verb) || !allows(committed, "", "secrets", verb) {
			t.Errorf("the operator role and the committed role must allow %s on secrets", verb)
		}
	}
}
