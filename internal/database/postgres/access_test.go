package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testVerifier = "SCRAM-SHA-256$4096:W22ZaJ0SNY7soEsUEjb6gQ==$WG5d8oPm3OtcPnkdi4Uo7BkeZkBFzpcXkuLmtbsT4qY=:wfPLwcE6nTWhTAmQ7tl2KeoiWGPlZqQxSrmfPwDl2dU="

func accessScripts(timeout time.Duration) map[string]func() (string, error) {
	grants := []Grant{{Database: "app", Level: "readwrite"}, {Database: "orders", Level: "readonly"}}
	return map[string]func() (string, error){
		"postgres_create_user": func() (string, error) { return CreateUserScript("alice", testVerifier, grants, timeout) },
		"postgres_delete_user": func() (string, error) { return DeleteUserScript("alice", []string{"app", "orders"}, timeout) },
		"postgres_set_grants": func() (string, error) {
			return SetGrantsScript("alice", []string{"app", "orders"}, grants[:1], timeout)
		},
		"postgres_reset_password":  func() (string, error) { return ResetPasswordScript("alice", testVerifier, timeout) },
		"postgres_create_database": func() (string, error) { return CreateDatabaseScript("orders", timeout) },
		"postgres_delete_database": func() (string, error) { return DeleteDatabaseScript("orders", timeout) },
	}
}

func TestAccessScriptsGolden(t *testing.T) {
	for name, build := range accessScripts(testTimeout) {
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		golden(t, name, []byte(script))
	}
}

func TestAccessScriptsEqualTheDadehatGoldensAtTheSameTimeout(t *testing.T) {
	for name, build := range accessScripts(30 * time.Second) {
		want, err := os.ReadFile(filepath.Join("testdata", "dadehat", name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != string(want) {
			t.Errorf("%s differs from the Dadehat golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
}

func TestDeleteUserLoopsEveryDatabaseAndStopsSessionsFirst(t *testing.T) {
	script, _ := DeleteUserScript("alice", []string{"app", "orders"}, testTimeout)
	for _, database := range []string{"app", "orders", "postgres"} {
		if !strings.Contains(script, connect(`"`+database+`"`, testTimeout)+`REASSIGN OWNED BY "alice" TO "dbadmin"`) {
			t.Errorf("no REASSIGN OWNED in %s", database)
		}
	}
	if strings.Index(script, "pg_terminate_backend") > strings.Index(script, "DROP ROLE") {
		t.Error("sessions must end before DROP ROLE")
	}
}

var hostile = []string{
	"a'b", "a\"b", "a\\b", "a;b", "a b", "a\nb", "a\x00b", "a~b", "a*b", "a&b", "a+b", "A", "", "1ab",
	`x"; DROP ROLE postgres; --`, "x'; SELECT 1; --",
}

func TestHostileNamesAreRefused(t *testing.T) {
	for _, name := range hostile {
		if _, err := CreateUserScript(name, testVerifier, nil, testTimeout); err == nil {
			t.Errorf("user %q accepted", name)
		}
		if _, err := CreateUserScript("alice", testVerifier, []Grant{{Database: name, Level: LevelAll}}, testTimeout); err == nil {
			t.Errorf("grant database %q accepted", name)
		}
		if _, err := CreateDatabaseScript(name, testTimeout); err == nil {
			t.Errorf("database %q accepted", name)
		}
		if _, err := DeleteDatabaseScript(name, testTimeout); err == nil {
			t.Errorf("delete database %q accepted", name)
		}
		if _, err := DeleteUserScript("alice", []string{name}, testTimeout); err == nil {
			t.Errorf("delete loop database %q accepted", name)
		}
	}
	for _, verifier := range append(append([]string{}, hostile...), "SCRAM-SHA-256$4096:a$b:c'; DROP ROLE x; --") {
		if verifier == "" {
			continue
		}
		if _, err := ResetPasswordScript("alice", verifier, testTimeout); err == nil {
			t.Errorf("verifier %q accepted", verifier)
		}
	}
}

func TestUnknownGrantLevelIsRefused(t *testing.T) {
	if _, err := CreateUserScript("alice", testVerifier, []Grant{{Database: "app", Level: "owner"}}, testTimeout); err == nil {
		t.Fatal("a level that does not exist was accepted")
	}
}

func FuzzCreateUserScript(f *testing.F) {
	for _, name := range hostile {
		f.Add(name, name)
	}
	f.Add("alice", "orders")
	f.Fuzz(func(t *testing.T, user, database string) {
		script, err := CreateUserScript(user, testVerifier, []Grant{{Database: database, Level: LevelReadWrite}}, testTimeout)
		if err != nil {
			return
		}
		if !identPattern.MatchString(user) || !identPattern.MatchString(database) {
			t.Fatalf("accepted %q %q", user, database)
		}
		if strings.Count(strings.ReplaceAll(script, preamble(testTimeout), ""), "'") != 2 {
			t.Fatalf("script has quotes outside the verifier: %q", script)
		}
		if strings.Count(script, `"`)%2 != 0 || strings.Contains(script, "\x00") {
			t.Fatalf("unbalanced identifier quoting: %q", script)
		}
	})
}

func TestReservedRolesAreRefusedEverywhere(t *testing.T) {
	for _, name := range []string{"dbadmin", "postgres", "streaming_replica", "cnpg_pooler_pgbouncer", "pg_monitor", "app_ro", "app_rw", "app_all"} {
		if _, err := CreateUserScript(name, testVerifier, nil, testTimeout); err == nil {
			t.Errorf("create %s", name)
		}
		if _, err := DeleteUserScript(name, []string{"app"}, testTimeout); err == nil {
			t.Errorf("delete %s", name)
		}
		if _, err := ResetPasswordScript(name, testVerifier, testTimeout); err == nil {
			t.Errorf("reset %s", name)
		}
		if _, err := SetGrantsScript(name, []string{"app"}, nil, testTimeout); err == nil {
			t.Errorf("grants %s", name)
		}
		if ReservedRole(name) == nil {
			t.Errorf("ReservedRole(%s)", name)
		}
	}
}

func TestReservedDatabases(t *testing.T) {
	for _, name := range []string{"postgres", "template0", "template1", "app"} {
		if !ReservedDatabase(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
	if ReservedDatabase("orders") {
		t.Error("orders is reserved")
	}
}

func TestParseDatabasesKeepsOnlyValidNames(t *testing.T) {
	got := ParseDatabases("app\n orders \nbad name\n\nUPPER\nshop_2\n")
	if strings.Join(got, ",") != "app,orders,shop_2" {
		t.Fatalf("databases %v", got)
	}
	if !strings.HasSuffix(ListDatabasesScript(testTimeout), "ORDER BY 1;\n") {
		t.Fatal("query shape")
	}
}
