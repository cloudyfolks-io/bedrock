package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
)

const testTimeout = 120 * time.Second

func TestBootstrapScriptGolden(t *testing.T) {
	script, err := BootstrapScript(testTimeout)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "script_bootstrap", []byte(script))
}

func TestBootstrapScriptUsesTheTimeout(t *testing.T) {
	script, _ := BootstrapScript(45 * time.Second)
	if !strings.HasPrefix(script, "SET statement_timeout = '45000';\nSET idle_in_transaction_session_timeout = '45000';\n") {
		t.Fatalf("script %q", script[:120])
	}
}

func TestResetAdminScriptHoldsAVerifierAndNeverThePassword(t *testing.T) {
	const password = "S3cretPasswordValue000000000000X"
	script, err := ResetAdminScript(password, testTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, password) {
		t.Fatal("the script holds the password")
	}
	if !strings.Contains(script, `ALTER ROLE "dbadmin" PASSWORD 'SCRAM-SHA-256$4096:`) || !strings.Contains(script, "pg_terminate_backend") {
		t.Fatalf("script %s", script)
	}
}

func TestResetPasswordScriptRefusesABadVerifierAndName(t *testing.T) {
	if _, err := resetPasswordScript("dbadmin", "plain-password", testTimeout); err == nil {
		t.Fatal("a plain password was accepted as a verifier")
	}
	if _, err := resetPasswordScript("Bad;Name", "SCRAM-SHA-256$4096:YQ==$YQ==:YQ==", testTimeout); err == nil {
		t.Fatal("a bad role name was accepted")
	}
}

func TestIdentifiersAndReservedRoles(t *testing.T) {
	for name, valid := range map[string]bool{"app": true, "orders_db": true, "a": false, "1abc": false, "Abc": false, "a-b": false, "a;b": false} {
		if ValidIdentifier(name) != valid {
			t.Errorf("%q valid=%v", name, !valid)
		}
	}
	for _, name := range []string{"postgres", "dbadmin", "pg_monitor", "app_ro", "app_rw", "app_all", "streaming_replica"} {
		if reserved(name) == nil {
			t.Errorf("%q is not reserved", name)
		}
	}
	if reserved("orders") != nil {
		t.Error("orders is reserved")
	}
}

func TestTargetAddressesThePrimaryAndHasNoSecretInTheCommand(t *testing.T) {
	target := Target("bedrock-dbaas", "uid-1")
	if target.Namespace != "bedrock-dbaas" || target.Container != "postgres" || target.Selector["cnpg.io/cluster"] != "uid-1" || target.Selector["cnpg.io/instanceRole"] != "primary" {
		t.Fatalf("target %+v", target)
	}
	command := strings.Join(target.Command, " ")
	if !strings.Contains(command, "psql") || strings.Contains(strings.ToLower(command), "password") {
		t.Fatalf("command %s", command)
	}
}

func TestClassifyMapsTheSQLStateCodes(t *testing.T) {
	cases := map[string]exec.Kind{
		"ERROR:  42710: role \"x\" already exists":                   exec.KindExists,
		"ERROR:  23505: duplicate key value":                         exec.KindExists,
		"ERROR:  42704: role \"x\" does not exist":                   exec.KindNotFound,
		"ERROR:  2BP01: role \"x\" cannot be dropped because depend": exec.KindOwnsObjects,
		"ERROR:  42601: syntax error":                                exec.KindUnclassified,
		"role already exists but no code":                            exec.KindUnclassified,
		"":                                                           exec.KindUnclassified,
	}
	for stderr, want := range cases {
		if got := Classify(stderr); got != want {
			t.Errorf("%q: %q, want %q", stderr, got, want)
		}
	}
}

func TestBootstrapScriptEqualsTheDadehatGoldenAtTheSameTimeout(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "dadehat", "postgres_bootstrap.golden"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := BootstrapScript(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("script differs from the Dadehat golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
