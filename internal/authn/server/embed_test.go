package server

import (
	"io/fs"
	"testing"
)

func TestEmbeddedUIHasIndexWhenBuilt(t *testing.T) {
	root, err := fs.Sub(UI, "ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 1 && entries[0].Name() == ".keep" {
		t.Skip("the UI is not built, only .keep is embedded")
	}
	if _, err := fs.Stat(root, "index.html"); err != nil {
		t.Fatalf("index.html missing from the embedded UI: %v", err)
	}
}
