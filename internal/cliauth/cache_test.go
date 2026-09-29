package cliauth

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachePath(t *testing.T) {
	sum := sha256.Sum256([]byte("https://sso.example.com"))
	want := filepath.Join("/home/alice", ".config", "bedrock", "tokens", hex.EncodeToString(sum[:])[:16]+".json")
	if got := CachePath("/home/alice", "https://sso.example.com"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWriteCacheMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens", "a.json")
	tokens := Tokens{AccessToken: "a", RefreshToken: "r", IDToken: "i", Expiry: time.Now().Round(0)}
	if err := WriteCache(path, tokens); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", info.Mode())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dirInfo.Mode())
	}
	got, err := ReadCache(path)
	if err != nil || !got.Expiry.Equal(tokens.Expiry) || got.AccessToken != tokens.AccessToken || got.RefreshToken != tokens.RefreshToken || got.IDToken != tokens.IDToken {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestFresh(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"far ahead", now.Add(time.Hour), true},
		{"just under a minute", now.Add(59 * time.Second), false},
		{"past", now.Add(-time.Minute), false},
	}
	for _, tc := range cases {
		if got := Fresh(Tokens{Expiry: tc.expiry}, now); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
