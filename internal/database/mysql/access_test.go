package mysql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const testHash = "0x244124303035616161616161616161616161616161616161"

func testGrants() []engine.Grant {
	return []engine.Grant{{Database: "a_b", Level: "readwrite"}, {Database: "axb", Level: "readonly"}, {Database: "app", Level: "all"}}
}

func TestScriptsGolden(t *testing.T) {
	cases := map[string]func() (string, error){
		"bootstrap":       func() (string, error) { return BootstrapScript(testHash) },
		"create_user":     func() (string, error) { return CreateUserScript("alice", testHash, testGrants()) },
		"set_grants":      func() (string, error) { return SetGrantsScript("alice", testGrants()[:1]) },
		"reset_password":  func() (string, error) { return ResetPasswordScript("alice", testHash) },
		"reset_admin":     func() (string, error) { return AdminPasswordScript(testHash) },
		"drop_user":       func() (string, error) { return DropUserScript("alice") },
		"create_database": func() (string, error) { return CreateDatabaseScript("a_b") },
		"delete_database": func() (string, error) { return DeleteDatabaseScript("a_b", []string{"alice"}) },
		"session_ids":     func() (string, error) { return SessionIDsQuery("alice") },
		"grantees":        func() (string, error) { return GranteesQuery("a_b") },
		"kill":            func() (string, error) { return KillScript([]int{12, 13}) },
		"list_users":      func() (string, error) { return ListUsersQuery, nil },
		"list_databases":  func() (string, error) { return ListDatabasesQuery, nil },
	}
	for name, build := range cases {
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		golden(t, "script_"+name, []byte(script))
	}
}

func TestScriptsMatchTheDadehatGolden(t *testing.T) {
	cases := map[string]func() (string, error){
		"mysql_bootstrap":       func() (string, error) { return BootstrapScript(testHash) },
		"mysql_create_user":     func() (string, error) { return CreateUserScript("alice", testHash, testGrants()) },
		"mysql_set_grants":      func() (string, error) { return SetGrantsScript("alice", testGrants()[:1]) },
		"mysql_reset_password":  func() (string, error) { return ResetPasswordScript("alice", testHash) },
		"mysql_delete_database": func() (string, error) { return DeleteDatabaseScript("a_b", []string{"alice"}) },
	}
	for name, build := range cases {
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", "dadehat", name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != script {
			t.Errorf("%s differs from the Dadehat golden.\n--- got ---\n%s\n--- want ---\n%s", name, script, want)
		}
	}
}

func TestUnderscoreStaysLiteralBecausePartialRevokesAreOn(t *testing.T) {
	script, _ := CreateUserScript("alice", testHash, []engine.Grant{{Database: "a_b", Level: "readonly"}})
	if !strings.Contains(script, "ON `a_b`.* TO 'alice'@'%'") {
		t.Fatalf("grant is wrong: %s", script)
	}
	if !strings.Contains(configuration(1024), "partial_revokes=ON") {
		t.Fatal("without partial revokes an underscore in a database name is a wildcard")
	}
}

func TestBootstrapNeverGrantsDangerousPrivileges(t *testing.T) {
	script, _ := BootstrapScript(testHash)
	grant := script[strings.Index(script, "GRANT CREATE USER"):]
	grant = grant[:strings.Index(grant, ";")]
	for _, bad := range []string{"SUPER", "FILE", "PROCESS", "SHUTDOWN", "SYSTEM_USER", "_ADMIN", "ALL PRIVILEGES"} {
		if strings.Contains(grant, bad) {
			t.Errorf("admin grant contains %s", bad)
		}
	}
	if !strings.Contains(script, "DROP USER IF EXISTS 'root'@'%'") {
		t.Error("root@% must be removed")
	}
}

var hostile = []string{"a'b", "a`b", "a\"b", "a\\b", "a;b", "a b", "a\nb", "a\x00b", "a~b", "a*b", "a&b", "a+b", "A", "", "1ab", "x`; DROP USER root; --"}

func TestHostileNamesAreRefused(t *testing.T) {
	for _, name := range hostile {
		if _, err := CreateUserScript(name, testHash, nil); err == nil {
			t.Errorf("user %q accepted", name)
		}
		if _, err := CreateUserScript("alice", testHash, []engine.Grant{{Database: name, Level: "all"}}); err == nil {
			t.Errorf("database %q accepted", name)
		}
		if _, err := CreateDatabaseScript(name); err == nil {
			t.Errorf("create database %q accepted", name)
		}
		if _, err := SessionIDsQuery(name); err == nil {
			t.Errorf("session query %q accepted", name)
		}
		if _, err := GranteesQuery(name); err == nil {
			t.Errorf("grantee query %q accepted", name)
		}
	}
	for _, hash := range []string{"'x'", "0xZZ", "0x", "0x12'; DROP USER root; --", ""} {
		if _, err := ResetPasswordScript("alice", hash); err == nil {
			t.Errorf("hash %q accepted", hash)
		}
		if _, err := BootstrapScript(hash); err == nil {
			t.Errorf("bootstrap hash %q accepted", hash)
		}
	}
}

func TestUnknownGrantLevelIsRefused(t *testing.T) {
	if _, err := CreateUserScript("alice", testHash, []engine.Grant{{Database: "app", Level: "owner"}}); err == nil {
		t.Fatal("level accepted")
	}
}

func FuzzCreateUserScript(f *testing.F) {
	for _, name := range hostile {
		f.Add(name, name)
	}
	f.Add("alice", "a_b")
	f.Fuzz(func(t *testing.T, user, database string) {
		script, err := CreateUserScript(user, testHash, []engine.Grant{{Database: database, Level: "readwrite"}})
		if err != nil {
			return
		}
		if !identRe.MatchString(user) || !identRe.MatchString(database) {
			t.Fatalf("accepted %q %q", user, database)
		}
		if strings.Count(script, "'")%2 != 0 || strings.Count(script, "`")%2 != 0 || strings.Contains(script, "\x00") {
			t.Fatalf("unbalanced quoting: %q", script)
		}
		if strings.Count(script, ";") != strings.Count(script, "\n") {
			t.Fatalf("statement count differs from line count: %q", script)
		}
	})
}

func TestReservedAccountsAreRefused(t *testing.T) {
	names := append([]string{"root", "dbadmin"}, InternalUsers...)
	for _, name := range names {
		if _, err := CreateUserScript(name, testHash, nil); err == nil {
			t.Errorf("create %s accepted", name)
		}
		if _, err := SetGrantsScript(name, nil); err == nil {
			t.Errorf("grants %s accepted", name)
		}
		if _, err := ResetPasswordScript(name, testHash); err == nil {
			t.Errorf("reset %s accepted", name)
		}
		if _, err := DropUserScript(name); err == nil {
			t.Errorf("drop %s accepted", name)
		}
	}
	if _, err := CreateUserScript("alice", testHash, []engine.Grant{{Database: "mysql", Level: "all"}}); err == nil {
		t.Error("grant on the mysql schema accepted")
	}
}

func TestKillScriptIsBounded(t *testing.T) {
	if _, err := KillScript(make([]int, MaxKill+1)); err == nil {
		t.Error("too many ids accepted")
	}
	if _, err := KillScript([]int{0}); err == nil {
		t.Error("id 0 accepted")
	}
}

func TestClassifyUsesErrorNumbers(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"ERROR 1007 (HY000) at line 1: Can't create database 'x'; database exists", engine.ErrExists},
		{"ERROR 1008 (HY000) at line 1: Can't drop database 'x'; database doesn't exist", engine.ErrNotFound},
		{"ERROR 1049 (42000): Unknown database 'x'", engine.ErrNotFound},
		{"ERROR 1507 (HY000): Error in list of partitions", engine.ErrNotFound},
		{"ERROR 1133 (42000): Can't find any matching row in the user table", engine.ErrNotFound},
		{"ERROR 1396 (HY000) at line 1: Operation CREATE USER failed for 'a'@'%'", engine.ErrExists},
		{"ERROR 1396 (HY000) at line 2: Operation DROP USER failed for 'a'@'%'", engine.ErrNotFound},
		{"ERROR 1396 (HY000) at line 2: Operation ALTER USER failed for 'a'@'%'", engine.ErrNotFound},
		{"ERROR 1044 (42000): Access denied", engine.ErrUnavailable},
		{"ERROR 1045 (28000): Access denied for user", engine.ErrUnavailable},
		{"ERROR 1064 (42000) at line 1: You have an error", engine.ErrUnavailable},
		{"Operation CREATE USER failed", engine.ErrExists},
		{"Unknown database 'x'", engine.ErrNotFound},
	}
	for _, c := range cases {
		if got := Classify(c.stderr); !errors.Is(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.stderr, got, c.want)
		}
	}
	if Classify("some other text") != nil || Classify("ERROR 9999 (HY000): odd") != nil {
		t.Error("unknown text classified")
	}
}

func TestListUsersGroupsRows(t *testing.T) {
	out := "alice\tapp\tN\tY\nalice\ta\\_b\tN\tN\nbob\t\t\t\nbad name\tx\tN\tN\n"
	users, err := ListUsers(context.Background(), &fakeRunner{outputs: []string{out}})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || len(users[0].Grants) != 2 || users[0].Grants[0].Level != "readwrite" || users[0].Grants[1].Database != "a_b" || len(users[1].Grants) != 0 {
		t.Fatalf("users %+v", users)
	}
}

func TestListDatabasesKeepsValidIdentifiers(t *testing.T) {
	databases, err := ListDatabases(context.Background(), &fakeRunner{outputs: []string{"app\nbad name\na_b\n"}})
	if err != nil || len(databases) != 2 || databases[0] != "app" || databases[1] != "a_b" {
		t.Fatalf("databases %v %v", databases, err)
	}
}

func TestDeleteUserDropsThenKillsSessions(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"", "12\n13\nnot-a-number\n"}}
	if err := DeleteUser(context.Background(), runner, "alice"); err != nil {
		t.Fatal(err)
	}
	if len(runner.scripts) != 4 || !strings.HasPrefix(runner.scripts[0], "DROP USER 'alice'@'%'") || runner.scripts[2] != "KILL CONNECTION 12;\n" || runner.scripts[3] != "KILL CONNECTION 13;\n" {
		t.Fatalf("scripts %q", runner.scripts)
	}
}

func TestDeleteDatabaseRevokesEveryGrantee(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"alice\nbob\nbad name\n"}}
	if err := DeleteDatabase(context.Background(), runner, "a_b"); err != nil {
		t.Fatal(err)
	}
	want := "REVOKE ALL PRIVILEGES ON `a_b`.* FROM 'alice'@'%';\nREVOKE ALL PRIVILEGES ON `a_b`.* FROM 'bob'@'%';\nDROP DATABASE `a_b`;\n"
	if len(runner.scripts) != 2 || runner.scripts[1] != want {
		t.Fatalf("scripts %q", runner.scripts)
	}
}

func TestOperationsRunTheirScriptsAndStopOnError(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	if err := Bootstrap(ctx, runner, "Abcdefghijklmnop1234"); err != nil {
		t.Fatal(err)
	}
	if err := ResetAdmin(ctx, runner, "Abcdefghijklmnop1234"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(ctx, runner, engine.UserSpec{Name: "alice", Password: "Abcdefghijklmnop1234", Grants: testGrants()[:1]}); err != nil {
		t.Fatal(err)
	}
	if err := SetGrants(ctx, runner, "alice", testGrants()); err != nil {
		t.Fatal(err)
	}
	if err := CreateDatabase(ctx, runner, "a_b"); err != nil {
		t.Fatal(err)
	}
	if err := ResetPassword(ctx, runner, "alice", "Abcdefghijklmnop1234"); err != nil {
		t.Fatal(err)
	}
	prefixes := []string{"DROP USER IF EXISTS 'root'", "ALTER USER 'dbadmin'", "CREATE USER 'alice'", "REVOKE ALL PRIVILEGES, GRANT OPTION", "CREATE DATABASE `a_b`", "ALTER USER 'alice'"}
	for index, prefix := range prefixes {
		if !strings.HasPrefix(runner.scripts[index], prefix) {
			t.Errorf("script %d is %q, want prefix %q", index, runner.scripts[index], prefix)
		}
	}
	failing := &fakeRunner{err: errors.New("boom")}
	if err := CreateUser(ctx, failing, engine.UserSpec{Name: "alice", Password: "x"}); err == nil {
		t.Error("runner error swallowed")
	}
	if err := CreateUser(ctx, runner, engine.UserSpec{Name: "root", Password: "x"}); err == nil {
		t.Error("reserved user created")
	}
}

func TestNoPasswordReachesTheRunnerInPlainText(t *testing.T) {
	password := "Plain-Text-Password-1234567890"
	runner := &fakeRunner{}
	ctx := context.Background()
	_ = Bootstrap(ctx, runner, password)
	_ = ResetAdmin(ctx, runner, password)
	_ = CreateUser(ctx, runner, engine.UserSpec{Name: "alice", Password: password})
	_ = ResetPassword(ctx, runner, "alice", password)
	if len(runner.scripts) == 0 {
		t.Fatal("nothing ran")
	}
	for _, script := range runner.scripts {
		if strings.Contains(script, password) {
			t.Fatalf("script holds the password: %s", script)
		}
	}
	if strings.Contains(strings.Join(Target(testUID).Command, " "), password) {
		t.Fatal("argv holds the password")
	}
}

func TestSuperuserHoldsRootAndTheInternalUsers(t *testing.T) {
	data, err := Superuser("root-secret")
	if err != nil {
		t.Fatal(err)
	}
	if string(data["root"]) != "root-secret" || len(data) != 9 {
		t.Fatalf("keys %d", len(data))
	}
	seen := map[string]bool{}
	for _, user := range InternalUsers {
		password := string(data[user])
		if len(password) != 32 || seen[password] {
			t.Errorf("password of %s: %q", user, password)
		}
		seen[password] = true
	}
}
