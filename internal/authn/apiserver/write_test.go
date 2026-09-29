package apiserver

import (
	"os"
	"path/filepath"
	"slices"
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
		if !slices.Contains(owned, path) {
			t.Fatalf("owner not applied to %s: %v", path, owned)
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
