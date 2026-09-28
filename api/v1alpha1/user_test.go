package v1alpha1

import (
	"strings"
	"testing"
)

func TestNormalizeUsername(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "already normalized", raw: "alice", want: "alice"},
		{name: "uppercase", raw: "Alice", want: "alice"},
		{name: "surrounding whitespace", raw: " alice ", want: "alice"},
		{name: "ldap style name", raw: "T.Farahani", want: "t.farahani"},
		{name: "email", raw: " Alice@Example.com ", want: "alice@example.com"},
		{name: "inner whitespace refused", raw: "ali ce", wantErr: true},
		{name: "unicode look-alike refused", raw: "аlice", wantErr: true},
		{name: "too long refused", raw: strings.Repeat("a", 300), wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeUsername(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("NormalizeUsername(%q) = %q, want an error", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeUsername(%q): %v", c.raw, err)
			}
			if got != c.want {
				t.Fatalf("NormalizeUsername(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
	alice, _ := NormalizeUsername("Alice")
	spaced, _ := NormalizeUsername(" alice ")
	plain, _ := NormalizeUsername("alice")
	if alice != spaced || spaced != plain {
		t.Fatalf("Alice, ` alice ` and alice must normalize to the same username: %q %q %q", alice, spaced, plain)
	}
}

func TestUserObjectName(t *testing.T) {
	if got := UserObjectName("t.farahani"); got != "t.farahani" {
		t.Fatalf("a short subdomain must be unchanged, got %q", got)
	}
	long := strings.Repeat("a", 64)
	got := UserObjectName(long)
	if !strings.HasPrefix(got, "u-") || len(got) != 22 {
		t.Fatalf("a subdomain over 63 characters must hash, got %q", got)
	}
	email := "alice@example.com"
	gotEmail := UserObjectName(email)
	if !strings.HasPrefix(gotEmail, "u-") || len(gotEmail) != 22 {
		t.Fatalf("an email address must hash, got %q", gotEmail)
	}
	if UserObjectName(email) != gotEmail {
		t.Fatal("UserObjectName must be deterministic")
	}
	if UserObjectName(long) == gotEmail {
		t.Fatal("two different usernames must not collide in this test")
	}
}

func TestCredentialName(t *testing.T) {
	if got := CredentialName("alice", MethodTOTP); got != "alice-totp" {
		t.Fatalf("CredentialName = %q", got)
	}
}
