package objectstorage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectStorageDocNamesTheAdoptContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "object-storage.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, name := range []string{AccountNameAnnotation, AdoptUserAnnotation, reasonAccountNameTaken, reasonAdoptUserMissing, reasonUserClaimed, "bedrock:bedrock-admins"} {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/object-storage.md does not mention %q", name)
		}
	}
}
