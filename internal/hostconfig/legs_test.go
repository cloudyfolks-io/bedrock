package hostconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/pkgmgr"
)

var productionLegs = []v1alpha1.TenantLegSpec{
	{Interface: "br-public.30", AllowSources: []string{"100.65.0.1/32"}},
	{Interface: "br-public.20"},
}

func TestRenderLegsRulesetGolden(t *testing.T) {
	got, err := RenderLegsRuleset(productionLegs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "tenant-legs.nft"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("ruleset differs from testdata/tenant-legs.nft:\n%s", got)
	}
}

func TestRenderLegsRulesetIsIndependentOfOrder(t *testing.T) {
	first, _ := RenderLegsRuleset(productionLegs)
	second, _ := RenderLegsRuleset([]v1alpha1.TenantLegSpec{productionLegs[1], productionLegs[0]})
	if first != second {
		t.Fatal("order of the legs changed the ruleset")
	}
}

func TestRenderLegsRulesetSplitsAddressFamilies(t *testing.T) {
	got, err := RenderLegsRuleset([]v1alpha1.TenantLegSpec{{Interface: "leg0", AllowSources: []string{"10.1.2.3/24", "fd00::1/64"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`iifname "leg0" ip saddr { 10.1.2.0/24 } accept`, `iifname "leg0" ip6 saddr { fd00::/64 } accept`} {
		if !strings.Contains(got, line) {
			t.Fatalf("missing %q in\n%s", line, got)
		}
	}
}

func TestRenderLegsRulesetRejectsUnsafeInput(t *testing.T) {
	for name, leg := range map[string]v1alpha1.TenantLegSpec{
		"quote in interface":  {Interface: `a" accept #`},
		"empty interface":     {},
		"long interface":      {Interface: "abcdefghijklmnop"},
		"source not a prefix": {Interface: "leg0", AllowSources: []string{"1.2.3.4 accept"}},
	} {
		if _, err := RenderLegsRuleset([]v1alpha1.TenantLegSpec{leg}); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func legsDeps(root string, exec *host.FakeExec) Deps {
	return Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}
}

func legsExec() *host.FakeExec {
	return &host.FakeExec{ResponsePrefixes: map[string]string{"systemctl ": "", "nft ": "", "sysctl -p ": ""}}
}

func TestLegsStepInstallsGuardBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	exec := legsExec()
	message, err := legsStep(context.Background(), legsDeps(root, exec), v1alpha1.HostConfigSpec{TenantLegs: productionLegs})
	if err != nil {
		t.Fatal(err)
	}
	if message == "" {
		t.Fatal("empty message")
	}
	unit, err := os.ReadFile(filepath.Join(root, "etc", "systemd", "system", "bedrock-tenant-legs.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"DefaultDependencies=no", "Before=network-pre.target", "Wants=network-pre.target", "ExecStart=/usr/sbin/nft -f /etc/bedrock/tenant-legs.nft", "WantedBy=multi-user.target"} {
		if !strings.Contains(string(unit), line) {
			t.Fatalf("unit lacks %q:\n%s", line, unit)
		}
	}
	rules, _ := os.ReadFile(filepath.Join(root, "etc", "bedrock", "tenant-legs.nft"))
	want, _ := RenderLegsRuleset(productionLegs)
	if string(rules) != want {
		t.Fatal("rule file differs from the rendered ruleset")
	}
	if strings.Join(exec.Calls, "|") != "systemctl daemon-reload|systemctl enable --now bedrock-tenant-legs.service" {
		t.Fatalf("calls %v", exec.Calls)
	}
}

func TestLegsStepIsIdempotent(t *testing.T) {
	root := t.TempDir()
	spec := v1alpha1.HostConfigSpec{TenantLegs: productionLegs}
	if _, err := legsStep(context.Background(), legsDeps(root, legsExec()), spec); err != nil {
		t.Fatal(err)
	}
	exec := legsExec()
	if _, err := legsStep(context.Background(), legsDeps(root, exec), spec); err != nil {
		t.Fatal(err)
	}
	if strings.Join(exec.Calls, "|") != "systemctl daemon-reload|systemctl enable --now bedrock-tenant-legs.service" {
		t.Fatalf("second run must only confirm the unit, calls %v", exec.Calls)
	}
}

func TestLegsStepReplacesTableInOneTransaction(t *testing.T) {
	root := t.TempDir()
	if _, err := legsStep(context.Background(), legsDeps(root, legsExec()), v1alpha1.HostConfigSpec{TenantLegs: productionLegs[:1]}); err != nil {
		t.Fatal(err)
	}
	exec := legsExec()
	if _, err := legsStep(context.Background(), legsDeps(root, exec), v1alpha1.HostConfigSpec{TenantLegs: productionLegs}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(exec.Calls, "|") != "systemctl daemon-reload|systemctl enable --now bedrock-tenant-legs.service|systemctl restart bedrock-tenant-legs.service" {
		t.Fatalf("calls %v", exec.Calls)
	}
	rules, _ := os.ReadFile(filepath.Join(root, "etc", "bedrock", "tenant-legs.nft"))
	text := string(rules)
	if !strings.HasPrefix(text, "table inet bedrock_legs\ndelete table inet bedrock_legs\n\ntable inet bedrock_legs {") {
		t.Fatalf("the file must create, delete and recreate the table in one transaction:\n%s", text)
	}
}

func TestLegsStepDoesNothingWithoutLegs(t *testing.T) {
	exec := &host.FakeExec{}
	message, err := legsStep(context.Background(), legsDeps(t.TempDir(), exec), v1alpha1.HostConfigSpec{})
	if err != nil || message == "" {
		t.Fatalf("message %q err %v", message, err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("calls %v", exec.Calls)
	}
}

func TestLegsStepRetiresTheGuardWhenLegsAreRemoved(t *testing.T) {
	root := t.TempDir()
	if _, err := legsStep(context.Background(), legsDeps(root, legsExec()), v1alpha1.HostConfigSpec{TenantLegs: productionLegs}); err != nil {
		t.Fatal(err)
	}
	exec := legsExec()
	if _, err := legsStep(context.Background(), legsDeps(root, exec), v1alpha1.HostConfigSpec{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(exec.Calls, "|") != "systemctl disable --now bedrock-tenant-legs.service|nft delete table inet bedrock_legs|systemctl daemon-reload" {
		t.Fatalf("calls %v", exec.Calls)
	}
	for _, path := range []string{"etc/bedrock/tenant-legs.nft", "etc/systemd/system/bedrock-tenant-legs.service"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed, err %v", path, err)
		}
	}
}

func TestLegsStepFailsOnUnsafeLeg(t *testing.T) {
	exec := &host.FakeExec{}
	_, err := legsStep(context.Background(), legsDeps(t.TempDir(), exec), v1alpha1.HostConfigSpec{TenantLegs: []v1alpha1.TenantLegSpec{{Interface: "a b"}}})
	if err == nil || len(exec.Calls) != 0 {
		t.Fatalf("err %v calls %v", err, exec.Calls)
	}
}
