package apiserver

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etc", "bedrock", "authn")
	var owned []string
	owner := func(path string) error {
		owned = append(owned, path)
		return nil
	}
	files := map[string][]byte{AuthenticationFile: []byte("a"), WebhookFile: []byte("w")}
	changed, err := WriteFiles(dir, files, owner)
	if err != nil || !changed {
		t.Fatalf("first write changed %v err %v", changed, err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", info, err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(content) {
			t.Fatalf("%s = %q %v", name, got, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", name, info, err)
		}
		if !ownedTempFile(owned, name) {
			t.Fatalf("owner not applied to the temp file for %s before rename: %v", name, owned)
		}
	}
	if !slices.Contains(owned, dir) {
		t.Fatalf("owner not applied to the directory: %v", owned)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("no temporary file may stay: %v %v", entries, err)
	}
	changed, err = WriteFiles(dir, files, KeepOwner)
	if err != nil || changed {
		t.Fatalf("same content changed %v err %v", changed, err)
	}
	if err := os.Chmod(filepath.Join(dir, WebhookFile), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err = WriteFiles(dir, map[string][]byte{AuthenticationFile: []byte("a"), WebhookFile: []byte("w2")}, KeepOwner)
	if err != nil || !changed {
		t.Fatalf("new content changed %v err %v", changed, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, WebhookFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("a rewritten file is 0600, got %v", info.Mode().Perm())
	}
}

func ownedTempFile(owned []string, name string) bool {
	prefix := "." + name + "."
	for _, path := range owned {
		if strings.HasPrefix(filepath.Base(path), prefix) {
			return true
		}
	}
	return false
}

func TestWriteFilesOwnerFailureLeavesOldFileLive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etc", "bedrock", "authn")
	if _, err := WriteFiles(dir, map[string][]byte{AuthenticationFile: []byte("old")}, KeepOwner); err != nil {
		t.Fatal(err)
	}
	failing := func(path string) error {
		if path == dir {
			return nil
		}
		return errors.New("chown failed")
	}
	changed, err := WriteFiles(dir, map[string][]byte{AuthenticationFile: []byte("new")}, failing)
	if err == nil || changed {
		t.Fatalf("owner failure must report changed=false and an error: changed %v err %v", changed, err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, AuthenticationFile))
	if readErr != nil || string(got) != "old" {
		t.Fatalf("the old file must stay live: %q %v", got, readErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("no temporary file may stay after an owner failure: %v %v", entries, err)
	}
}
