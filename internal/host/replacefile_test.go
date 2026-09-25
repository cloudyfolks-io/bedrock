package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "unit.service")
	cases := []struct {
		name     string
		content  string
		replaced bool
	}{
		{"new file", "a", false},
		{"same content", "a", false},
		{"new content", "b", true},
	}
	for _, tc := range cases {
		replaced, err := ReplaceFile(path, tc.content, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if replaced != tc.replaced {
			t.Fatalf("%s: replaced %v, want %v", tc.name, replaced, tc.replaced)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != tc.content {
			t.Fatalf("%s: content %q, err %v", tc.name, got, err)
		}
	}
}
