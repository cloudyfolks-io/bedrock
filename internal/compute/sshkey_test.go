package compute

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func marshaled(t *testing.T, key any) string {
	t.Helper()
	public, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
}

func ed25519Key(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return marshaled(t, public)
}

func ecdsaKey(t *testing.T, curve elliptic.Curve) string {
	t.Helper()
	private, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return marshaled(t, &private.PublicKey)
}

func rsaKey(t *testing.T, bits int) string {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return marshaled(t, &private.PublicKey)
}

func TestAuthorizedKeyAcceptsSupportedTypes(t *testing.T) {
	keys := map[string]string{
		"ed25519": ed25519Key(t),
		"p256":    ecdsaKey(t, elliptic.P256()),
		"p384":    ecdsaKey(t, elliptic.P384()),
		"p521":    ecdsaKey(t, elliptic.P521()),
		"rsa2048": rsaKey(t, 2048),
	}
	for name, key := range keys {
		for _, raw := range []string{key, key + " alice@laptop", "  " + key + "  ", key + "\n"} {
			got, err := AuthorizedKey(raw)
			if err != nil || got != key {
				t.Errorf("%s: %q gave %q, %v", name, raw, got, err)
			}
		}
	}
}

func TestAuthorizedKeyRefuses(t *testing.T) {
	good := ed25519Key(t)
	cases := map[string]string{
		"empty":          "",
		"garbage base64": "ssh-ed25519 !!!!",
		"options":        `command="id" ` + good,
		"two lines":      good + "\n" + good,
		"comment line":   good + "\n# note",
		"dsa":            "ssh-dss AAAAB3NzaC1kc3MAAACBAP",
		"small rsa":      rsaKey(t, 1024),
		"certificate":    "ssh-ed25519-cert-v01@openssh.com AAAA",
		"carriage":       good + "\r",
		"oversize":       good + " " + strings.Repeat("a", 16384),
	}
	for name, raw := range cases {
		if got, err := AuthorizedKey(raw); err == nil {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}
}

func TestAuthorizedKeyDropsTheComment(t *testing.T) {
	good := ed25519Key(t)
	got, err := AuthorizedKey(good + ` x" # y: - z`)
	if err != nil || got != good {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(strings.Fields(got)) != 2 {
		t.Fatalf("got %q", got)
	}
}
