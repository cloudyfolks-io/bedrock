package secret

import (
	"crypto/rand"
	"strings"
	"testing"
)

func TestHashRoundTrip(t *testing.T) {
	encoded, err := Hash(rand.Reader, DefaultParams, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	ok, err := Verify(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("Verify(correct) = %v, %v", ok, err)
	}
	ok, err = Verify(encoded, "wrong password")
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v", ok, err)
	}
}

func TestVerifyReadsParametersFromTheHash(t *testing.T) {
	weak := Params{Time: 1, MemoryKiB: 8 * 1024, Threads: 1, SaltLen: 16, KeyLen: 32}
	encoded, err := Hash(rand.Reader, weak, "a weaker password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("hash does not carry its own parameters: %q", encoded)
	}
	ok, err := Verify(encoded, "a weaker password")
	if err != nil || !ok {
		t.Fatalf("Verify with embedded weak parameters = %v, %v", ok, err)
	}
}

func TestVerifyRefusesMalformed(t *testing.T) {
	validSalt := "AAAAAAAAAAAAAAAAAAAAAA"
	validKey := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cases := map[string]string{
		"empty":            "",
		"wrong algorithm":  "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"wrong version":    "$argon2id$v=1$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"bad parameters":   "$argon2id$v=19$bogus$c2FsdA$aGFzaA",
		"bad salt":         "$argon2id$v=19$m=65536,t=3,p=2$not-base64!$aGFzaA",
		"bad key":          "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$not-base64!",
		"memory too large": "$argon2id$v=19$m=4194304,t=3,p=2$" + validSalt + "$" + validKey,
		"time too large":   "$argon2id$v=19$m=65536,t=11,p=2$" + validSalt + "$" + validKey,
		"zero threads":     "$argon2id$v=19$m=65536,t=3,p=0$" + validSalt + "$" + validKey,
		"salt too short":   "$argon2id$v=19$m=65536,t=3,p=2$AAAAAA$" + validKey,
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(encoded, "anything"); err == nil {
				t.Fatalf("Verify(%q) must fail", encoded)
			}
		})
	}
}
