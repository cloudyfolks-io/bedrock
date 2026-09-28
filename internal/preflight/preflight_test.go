package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func goodFacts() host.Facts {
	return host.Facts{OSID: "ubuntu", OSVersionID: "24.04", Arch: "amd64", Root: true, Systemd: true, CgroupV2: true, KVM: true, NTPSynced: true, DefaultInterface: "bond0", DefaultIP: "10.0.10.11", Interfaces: []string{"lo", "bond0"}, FreeVarLibBytes: 100 << 30, IOMMUGroups: 1}
}

func goodConfig() v1alpha1.ClusterConfig {
	var cfg v1alpha1.ClusterConfig
	cfg.Spec.API = v1alpha1.APISpec{VIP: "10.0.10.10", VIPMode: "arp"}
	cfg.Spec.Network.ManagementInterface = "bond0"
	cfg.Spec.Storage.Devices = []string{"/dev/sdb"}
	return cfg
}

func goodExec() *host.FakeExec {
	return &host.FakeExec{
		Responses: map[string]string{"ip -json addr": `[{"addr_info":[{"family":"inet","local":"10.0.10.11"}]}]`},
		Errors:    map[string]error{"ping -c 1 -W 1 10.0.10.10": &host.ExitError{Code: 1}},
	}
}

func TestRunAllGood(t *testing.T) {
	results := Run(context.Background(), goodExec(), goodFacts(), []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	if Blocked(results) {
		t.Fatalf("unexpected block: %s", Format(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("result not ok: %+v", r)
		}
	}
}

func TestRunFatalFailures(t *testing.T) {
	facts := goodFacts()
	facts.Root = false
	facts.OSID = "gentoo"
	facts.Interfaces = []string{"lo"}
	cfg := goodConfig()
	cfg.Spec.API.VIP = "10.0.10.11"
	e := &host.FakeExec{Responses: map[string]string{"ip -json addr": `[{"addr_info":[{"family":"inet","local":"10.0.10.11"}]}]`}}
	results := Run(context.Background(), e, facts, []host.Device{{Path: "/dev/sdb", Block: true, Signature: "ext4"}}, cfg, []string{"ubuntu-24.04"})
	if !Blocked(results) {
		t.Fatal("expected block")
	}
	failed := map[string]bool{}
	for _, r := range results {
		if !r.OK && r.Fatal {
			failed[r.Name] = true
		}
	}
	for _, name := range []string{"root", "os", "management-interface", "vip", "device-/dev/sdb"} {
		if !failed[name] {
			t.Fatalf("expected %s to fail: %s", name, Format(results))
		}
	}
}

func TestRunWarningsDoNotBlock(t *testing.T) {
	facts := goodFacts()
	facts.KVM = false
	facts.NTPSynced = false
	facts.FreeVarLibBytes = 1 << 30
	facts.IOMMUGroups = 0
	results := Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	if Blocked(results) {
		t.Fatalf("warnings must not block: %s", Format(results))
	}
	out := Format(results)
	if !strings.Contains(out, "[warn] kvm") || !strings.Contains(out, "[warn] ntp") || !strings.Contains(out, "[warn] disk") || !strings.Contains(out, "[warn] iommu") {
		t.Fatalf("format %s", out)
	}
}

func TestRunNonBlockDevice(t *testing.T) {
	results := Run(context.Background(), goodExec(), goodFacts(), []host.Device{{Path: "/dev/sdb", Block: false}}, goodConfig(), []string{"ubuntu-24.04"})
	if !Blocked(results) {
		t.Fatal("non-block device must block")
	}
}

func TestRunVIPFreeFailsWhenVIPAnswersPing(t *testing.T) {
	e := &host.FakeExec{
		Responses: map[string]string{
			"ip -json addr":             "[]",
			"ping -c 1 -W 1 10.0.10.10": "",
		},
	}
	results := Run(context.Background(), e, goodFacts(), []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	if !Blocked(results) {
		t.Fatal("expected block when the vip already answers to ping")
	}
	for _, r := range results {
		if r.Name == "vip-free" && !r.OK {
			return
		}
	}
	t.Fatalf("expected vip-free to fail: %s", Format(results))
}

func TestRunVIPFreeSkippedWhenAlreadyAssignedToThisHost(t *testing.T) {
	e := &host.FakeExec{
		Responses: map[string]string{"ip -json addr": `[{"addr_info":[{"family":"inet","local":"10.0.10.10"}]}]`},
	}
	results := Run(context.Background(), e, goodFacts(), []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	if Blocked(results) {
		t.Fatalf("must not block when the vip is already on this host: %s", Format(results))
	}
}

func TestRunVIPFreeFailsWhenPingCannotRun(t *testing.T) {
	e := &host.FakeExec{
		Responses: map[string]string{"ip -json addr": "[]"},
		Errors:    map[string]error{"ping -c 1 -W 1 10.0.10.10": errors.New("exec: ping: not found")},
	}
	results := Run(context.Background(), e, goodFacts(), []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	for _, r := range results {
		if r.Name == "vip-free" && !r.OK && strings.Contains(r.Message, "cannot probe") {
			return
		}
	}
	t.Fatalf("expected vip-free to fail when ping cannot run: %s", Format(results))
}
