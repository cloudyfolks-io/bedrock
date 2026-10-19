package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func databaseDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "database.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDocsDescribeEveryReason(t *testing.T) {
	text := databaseDoc(t)
	for _, reason := range Reasons() {
		if !strings.Contains(text, "| `"+reason+"` |") {
			t.Errorf("docs/database.md has no table row for the reason %s", reason)
		}
	}
}

func TestReasonsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range Reasons() {
		if seen[reason] {
			t.Errorf("the reason %s is listed twice", reason)
		}
		seen[reason] = true
	}
}

func TestDocsNameEverySettingOfTheArea(t *testing.T) {
	text := databaseDoc(t)
	for _, def := range settings.Catalog() {
		if strings.HasPrefix(def.Key, "database.") && !strings.Contains(text, "| `"+def.Key+"` |") {
			t.Errorf("docs/database.md has no table row for the Setting %s", def.Key)
		}
	}
}

func TestDocsNameTheKindsTheSecretsAndTheQuotaKey(t *testing.T) {
	text := databaseDoc(t)
	for _, name := range []string{
		"DatabaseVersion", "DatabaseClass", "DatabaseUser", "DatabaseSchema", "bedrock-dbaas", "bedrock-database-ca", "database-backups-s3",
		"count/databases.bedrock.cloudyfolks.io", "tombstone-", "bedrock.cloudyfolks.io/reset-password", "bedrock.cloudyfolks.io/delete-after",
		"kube-vip.io/loadbalancerIPs", "<name>-admin", "<name>-db", "<name>-endpoint",
	} {
		if !strings.Contains(text, name) {
			t.Errorf("docs/database.md does not mention %s", name)
		}
	}
}
