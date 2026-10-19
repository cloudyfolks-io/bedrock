package registry

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnsureSecretsCreatesTheSixSecrets(t *testing.T) {
	c := fakeCluster(t)
	if err := EnsureSecrets(context.Background(), c, seeded(7)); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"harbor-admin":                {"HARBOR_ADMIN_PASSWORD"},
		"harbor-core-keys":            {"secret", "CSRF_KEY", "secretKey"},
		"harbor-jobservice-keys":      {"JOBSERVICE_SECRET"},
		"harbor-registry-keys":        {"REGISTRY_HTTP_SECRET"},
		"harbor-registry-credentials": {"REGISTRY_PASSWD", "REGISTRY_HTPASSWD"},
		"harbor-db-credentials":       {"username", "password"},
	}
	for name, keys := range want {
		secret := getSecret(t, c, name)
		for _, key := range keys {
			if len(secret.Data[key]) == 0 {
				t.Errorf("%s has no %s", name, key)
			}
		}
		if len(secret.Data) != len(keys) {
			t.Errorf("%s has keys %d, want %d", name, len(secret.Data), len(keys))
		}
	}
	if len(getSecret(t, c, "harbor-core-keys").Data["secretKey"]) != 16 {
		t.Error("secretKey must have 16 characters")
	}
	if getSecret(t, c, "harbor-db-credentials").Type != corev1.SecretTypeBasicAuth || string(getSecret(t, c, "harbor-db-credentials").Data["username"]) != "harbor" {
		t.Error("the database Secret is not a basic-auth Secret for harbor")
	}
}

func TestEnsureSecretsKeepsASecretThatExists(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "harbor-admin"},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte("imported-password")},
	}
	c := fakeCluster(t, existing)
	if err := EnsureSecrets(context.Background(), c, seeded(9)); err != nil {
		t.Fatal(err)
	}
	if got := string(getSecret(t, c, "harbor-admin").Data["HARBOR_ADMIN_PASSWORD"]); got != "imported-password" {
		t.Errorf("password %q", got)
	}
	if err := EnsureSecrets(context.Background(), c, seeded(1)); err != nil {
		t.Fatal(err)
	}
	if got := string(getSecret(t, c, "harbor-admin").Data["HARBOR_ADMIN_PASSWORD"]); got != "imported-password" {
		t.Errorf("password after a second run %q", got)
	}
}

func TestEnsureSecretsIsStableAcrossRuns(t *testing.T) {
	c := fakeCluster(t)
	if err := EnsureSecrets(context.Background(), c, seeded(3)); err != nil {
		t.Fatal(err)
	}
	first := string(getSecret(t, c, "harbor-core-keys").Data["secret"])
	if err := EnsureSecrets(context.Background(), c, seeded(200)); err != nil {
		t.Fatal(err)
	}
	if got := string(getSecret(t, c, "harbor-core-keys").Data["secret"]); got != first {
		t.Errorf("secret changed from %q to %q", first, got)
	}
}

func TestRegistryHtpasswdMatchesThePassword(t *testing.T) {
	c := fakeCluster(t)
	if err := EnsureSecrets(context.Background(), c, seeded(5)); err != nil {
		t.Fatal(err)
	}
	secret := getSecret(t, c, "harbor-registry-credentials")
	user, hash, found := strings.Cut(string(secret.Data["REGISTRY_HTPASSWD"]), ":")
	if !found || user != "harbor_registry_user" {
		t.Fatalf("htpasswd %q", secret.Data["REGISTRY_HTPASSWD"])
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), secret.Data["REGISTRY_PASSWD"]); err != nil {
		t.Errorf("the hash does not match the password: %v", err)
	}
}

func TestAdminPasswordHasEveryCharacterClass(t *testing.T) {
	for seed := range byte(20) {
		value, err := randomString(seeded(seed), 32, true)
		if err != nil || !hasEveryClass(value) || len(value) != 32 {
			t.Errorf("seed %d: password %q error %v", seed, value, err)
		}
	}
	if _, err := randomString(repeating(0), 32, true); err == nil {
		t.Error("a constant reader gave a password with every class")
	}
}

func TestRandomStringRejectsBiasedBytes(t *testing.T) {
	value, err := randomString(repeating(255), 8, false)
	if err == nil {
		t.Fatalf("a reader of rejected bytes gave %q", value)
	}
}

func TestRandomStringSurfacesAShortReader(t *testing.T) {
	if _, err := randomString(strings.NewReader("ab"), 8, false); err == nil {
		t.Fatal("a short reader gave no error")
	}
}

func TestSecretsUseOnlyLettersAndDigits(t *testing.T) {
	value, err := randomString(seeded(77), 64, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range value {
		if !strings.ContainsRune(alphabet, r) {
			t.Errorf("character %q is outside the alphabet", r)
		}
	}
}
