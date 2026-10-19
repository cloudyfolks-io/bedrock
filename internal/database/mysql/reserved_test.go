package mysql

import (
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func TestSchemaScriptsRefuseTheBootstrapAndSystemDatabases(t *testing.T) {
	for _, name := range []string{"app", "mysql", "sys", "information_schema", "performance_schema"} {
		if _, err := CreateDatabaseScript(name); err == nil {
			t.Errorf("create %s was accepted", name)
		}
		if _, err := DeleteDatabaseScript(name, nil); err == nil {
			t.Errorf("drop %s was accepted", name)
		}
		if _, err := GranteesQuery(name); err == nil {
			t.Errorf("grantee query of %s was accepted", name)
		}
	}
	if _, err := CreateDatabaseScript("orders"); err != nil {
		t.Fatal(err)
	}
}

func TestGrantsStillReachTheAppDatabase(t *testing.T) {
	if _, err := CreateUserScript("alice", "0x"+strings.Repeat("ab", 20), []engine.Grant{{Database: "app", Level: "readwrite"}}); err != nil {
		t.Fatal(err)
	}
}
