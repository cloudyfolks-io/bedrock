package valkey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

var readwrite = []engine.Grant{{Database: "app", Level: "readwrite"}}

func TestScriptsGolden(t *testing.T) {
	hash := PasswordHash("Abcdefghijklmnopqrstuvwxyz012345")
	cases := map[string]func() (string, error){
		"create_user": func() (string, error) { return CreateUserScript("alice", hash, readwrite, "app:") },
		"create_all": func() (string, error) {
			return CreateUserScript("bob", hash, []engine.Grant{{Database: "app", Level: "all"}}, "")
		},
		"create_readonly": func() (string, error) {
			return CreateUserScript("carol", hash, []engine.Grant{{Database: "app", Level: "readonly"}}, "orders:")
		},
		"set_grants": func() (string, error) {
			return SetGrantsScript("alice", []engine.Grant{{Database: "app", Level: "readonly"}}, "x")
		},
		"reset_password": func() (string, error) { return ResetPasswordScript("alice", hash) },
		"reset_admin":    func() (string, error) { return ResetPasswordScript("dbadmin", hash) },
		"delete_user":    func() (string, error) { return DeleteUserScript("alice") },
		"get_user":       func() (string, error) { return GetUserScript("alice") },
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
	hash := PasswordHash("Abcdefghijklmnopqrstuvwxyz012345")
	cases := map[string]func() (string, error){
		"valkey_create_user": func() (string, error) { return CreateUserScript("alice", hash, readwrite, "app:") },
		"valkey_create_all": func() (string, error) {
			return CreateUserScript("bob", hash, []engine.Grant{{Database: "app", Level: "all"}}, "")
		},
		"valkey_set_grants": func() (string, error) {
			return SetGrantsScript("alice", []engine.Grant{{Database: "app", Level: "readonly"}}, "x")
		},
		"valkey_reset_password": func() (string, error) { return ResetPasswordScript("alice", hash) },
		"valkey_delete_user":    func() (string, error) { return DeleteUserScript("alice") },
	}
	for name, build := range cases {
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := readDadehat(t, name)
		if want != script {
			t.Errorf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", name, script, want)
		}
	}
}

func TestEveryChangeIsFollowedBySave(t *testing.T) {
	hash := PasswordHash("x")
	for _, build := range []func() (string, error){
		func() (string, error) { return CreateUserScript("alice", hash, readwrite, "") },
		func() (string, error) { return SetGrantsScript("alice", readwrite, "") },
		func() (string, error) { return ResetPasswordScript("alice", hash) },
		func() (string, error) { return DeleteUserScript("alice") },
	} {
		script, _ := build()
		if !strings.HasSuffix(script, "ACL SAVE\n") {
			t.Errorf("script does not end with ACL SAVE: %q", script)
		}
	}
}

func TestLevelRulesNeverAllowDangerousCommands(t *testing.T) {
	for level, rules := range levelRules {
		if !strings.Contains(rules, "-@dangerous") || !strings.Contains(rules, "-@admin") {
			t.Errorf("%s keeps @dangerous or @admin", level)
		}
	}
}

func TestKeyPrefixLimitsKeysAndChannels(t *testing.T) {
	script, err := CreateUserScript("alice", PasswordHash("x"), readwrite, "app:")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "resetkeys ~app:* resetchannels &app:*") {
		t.Fatalf("script %s", script)
	}
	open, _ := CreateUserScript("alice", PasswordHash("x"), readwrite, "")
	if !strings.Contains(open, "resetkeys ~* resetchannels &*") {
		t.Fatalf("script %s", open)
	}
}

func TestAPrefixOfMoreThan64CharactersIsRefused(t *testing.T) {
	hash := PasswordHash("x")
	if _, err := CreateUserScript("alice", hash, readwrite, strings.Repeat("p", 64)); err != nil {
		t.Fatalf("64 characters refused: %v", err)
	}
	if _, err := CreateUserScript("alice", hash, readwrite, strings.Repeat("p", 65)); err == nil {
		t.Fatal("65 characters accepted")
	}
	if _, err := SetGrantsScript("alice", readwrite, strings.Repeat("p", 65)); err == nil {
		t.Fatal("65 characters accepted by grants")
	}
}

var hostile = []string{"a'b", "a\"b", "a\\b", "a;b", "a b", "a\nb", "a\x00b", "a~b", "a*b", "a&b", "a+b", "A", "", "1ab", "x on nopass ~* +@all"}

func TestHostileInputsAreRefused(t *testing.T) {
	hash := PasswordHash("x")
	for _, bad := range hostile {
		if _, err := CreateUserScript(bad, hash, readwrite, ""); err == nil {
			t.Errorf("user %q accepted", bad)
		}
		if bad != "" && bad != "A" && bad != "1ab" {
			if _, err := CreateUserScript("alice", hash, readwrite, bad); err == nil {
				t.Errorf("prefix %q accepted", bad)
			}
		}
		if _, err := ResetPasswordScript("alice", bad); err == nil {
			t.Errorf("hash %q accepted", bad)
		}
	}
	if _, err := CreateUserScript("alice", hash, []engine.Grant{{Database: "other", Level: "all"}}, ""); err == nil {
		t.Error("grant on another database accepted")
	}
	if _, err := CreateUserScript("alice", hash, append(readwrite, readwrite...), ""); err == nil {
		t.Error("two grants accepted")
	}
	if _, err := CreateUserScript("alice", hash, []engine.Grant{{Database: "app", Level: "owner"}}, ""); err == nil {
		t.Error("unknown level accepted")
	}
}

func FuzzCreateUserScript(f *testing.F) {
	for _, bad := range hostile {
		f.Add(bad, bad)
	}
	f.Add("alice", "app:")
	hash := PasswordHash("x")
	f.Fuzz(func(t *testing.T, user, prefix string) {
		script, err := CreateUserScript(user, hash, readwrite, prefix)
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSuffix(script, "\n"), "\n")
		if len(lines) != 2 || lines[1] != "ACL SAVE" {
			t.Fatalf("unexpected script shape: %q", script)
		}
		if len(strings.Fields(lines[0])) != 17 {
			t.Fatalf("a field was injected: %q", lines[0])
		}
	})
}

func TestReservedUsersAreRefused(t *testing.T) {
	hash := PasswordHash("x")
	ctx := context.Background()
	for _, name := range []string{"default", "dbadmin", "_platform", "_exporter", "_abc"} {
		if _, err := CreateUserScript(name, hash, readwrite, ""); err == nil {
			t.Errorf("create %q accepted", name)
		}
		if _, err := SetGrantsScript(name, readwrite, ""); err == nil {
			t.Errorf("grants %q accepted", name)
		}
		if _, err := DeleteUserScript(name); err == nil {
			t.Errorf("delete %q accepted", name)
		}
		if err := ResetPassword(ctx, &fakeRunner{}, name, "x"); err == nil {
			t.Errorf("reset %q accepted", name)
		}
	}
	runner := &fakeRunner{}
	if err := ResetAdmin(ctx, runner, "x"); err != nil || !strings.Contains(runner.scripts[0], "ACL SETUSER dbadmin resetpass") {
		t.Fatalf("admin reset failed: %v %v", err, runner.scripts)
	}
}

func TestParseACLList(t *testing.T) {
	out := "user default off resetchannels -@all\nuser dbadmin on #abc ~* &* +@all -@admin\n" +
		"user alice on sanitize-payload #abc ~app:* resetchannels &app:* -@all +@read +@write +@keyspace\n" +
		"user bob on #abc ~* &* +@all -@dangerous\nuser carol on #abc ~* -@all +@read\nuser _platform on #abc ~* &* +@all\nuser _exporter on #abc ~* -@all +info\n"
	rows := ParseACLList(out)
	if len(rows) != 3 || rows[0].Level != "readwrite" || rows[0].Prefix != "app:" || rows[1].Level != "all" || rows[2].Level != "readonly" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestClassify(t *testing.T) {
	for _, text := range []string{"NOAUTH Authentication required.", "WRONGPASS invalid", "NOPERM x", "ERR bad", "ERR some NOPERM x"} {
		if !errors.Is(Classify(text), engine.ErrUnavailable) {
			t.Errorf("%q not classified as unavailable", text)
		}
	}
	if !errors.Is(Classify("ERR User alice already exists"), engine.ErrExists) {
		t.Error("exists not classified")
	}
	if !errors.Is(Classify("ERR No such user 'x'"), engine.ErrNotFound) {
		t.Error("not found not classified")
	}
	if Classify("fine") != nil {
		t.Error("plain text classified")
	}
}

func TestCheckRepliesStopsOnEveryErrorKind(t *testing.T) {
	if err := CheckReplies("OK\nOK\n"); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{
		"OK\nERR Error in ACL SETUSER modifier '~x': bad\nOK\n", "OK\n(error) NOPERM no\n", "NOAUTH Authentication required.\n",
		"WRONGPASS invalid username-password pair\n", "NOPERM this user has no permissions\n",
	} {
		if err := CheckReplies(reply); err == nil {
			t.Errorf("reply %q accepted", reply)
		}
	}
	runner := &fakeRunner{outputs: []string{"", "OK\nERR bad\n"}}
	err := CreateUser(context.Background(), runner, engine.UserSpec{Name: "alice", Password: "x", Grants: readwrite})
	if err == nil {
		t.Fatal("create hid an ERR reply")
	}
}

func TestCreateUserRefusesAnExistingUser(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"1) \"flags\"\n"}}
	err := CreateUser(context.Background(), runner, engine.UserSpec{Name: "alice", Password: "x", Grants: readwrite})
	if !engine.IsExists(err) || len(runner.scripts) != 1 {
		t.Fatalf("err=%v scripts=%d", err, len(runner.scripts))
	}
	runner = &fakeRunner{outputs: []string{"(nil)\n"}}
	if err := CreateUser(context.Background(), runner, engine.UserSpec{Name: "alice", Password: "x", Grants: readwrite}); err != nil || len(runner.scripts) != 2 {
		t.Fatalf("err=%v scripts=%d", err, len(runner.scripts))
	}
}

func TestNoPasswordReachesTheRunnerInPlainText(t *testing.T) {
	password := "Plain-Text-Password-1234567890"
	ctx := context.Background()
	runner := &fakeRunner{outputs: []string{"(nil)\n"}}
	_ = CreateUser(ctx, runner, engine.UserSpec{Name: "alice", Password: password, Grants: readwrite})
	_ = ResetPassword(ctx, runner, "alice", password)
	_ = ResetAdmin(ctx, runner, password)
	if len(runner.scripts) < 3 {
		t.Fatalf("scripts %q", runner.scripts)
	}
	for _, script := range runner.scripts {
		if strings.Contains(script, password) {
			t.Fatalf("script holds the password: %s", script)
		}
	}
	hashed := 0
	for _, script := range runner.scripts {
		hashed += strings.Count(script, "#"+PasswordHash(password))
	}
	if hashed != 3 {
		t.Fatalf("hash appears %d times", hashed)
	}
	if strings.Contains(strings.Join(Target(testUID).Command, " "), password) {
		t.Fatal("argv holds the password")
	}
}

func TestListUsersReturnsOneGrantOnTheDefaultDatabase(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"user alice on #abc ~app:* -@all +@read +@write\nuser dbadmin on #abc ~* +@all\n"}}
	users, err := ListUsers(context.Background(), runner)
	if err != nil || len(users) != 1 {
		t.Fatalf("users %+v %v", users, err)
	}
	if users[0].Name != "alice" || users[0].KeyPrefix != "app:" || len(users[0].Grants) != 1 || users[0].Grants[0] != (engine.Grant{Database: "app", Level: "readwrite"}) {
		t.Fatalf("user %+v", users[0])
	}
}

func TestSuperuserHoldsThePlatformAndTheMetricsPassword(t *testing.T) {
	data, err := Superuser("platform-secret")
	if err != nil {
		t.Fatal(err)
	}
	if string(data["password"]) != "platform-secret" || len(data["exporter-password"]) != 32 || len(data) != 2 {
		t.Fatalf("data %v", data)
	}
}

func TestRepliesOfTheRunnerErrorPassThrough(t *testing.T) {
	boom := errors.New("boom")
	if err := DeleteUser(context.Background(), &fakeRunner{err: boom}, "alice"); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if err := SetGrants(context.Background(), &fakeRunner{}, "alice", readwrite, "a:"); err != nil {
		t.Fatal(err)
	}
}
