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

func TestRunSwapBlocksAndNamesTheDevice(t *testing.T) {
	facts := goodFacts()
	facts.Swaps = []string{"/swap.img"}
	results := Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-24.04"})
	if !Blocked(results) {
		t.Fatal("swap must block")
	}
	out := Format(results)
	for _, want := range []string{"[fail] swap:", "/swap.img", "swapoff", "/etc/fstab"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestRunSwapMessageFitsTheDeviceKind(t *testing.T) {
	zram := swapMessage([]string{"/dev/zram0"})
	if !strings.Contains(zram, "zram") || strings.Contains(zram, "/etc/fstab") {
		t.Fatalf("zram message %q", zram)
	}
	file := swapMessage([]string{"/swap.img"})
	if !strings.Contains(file, "/etc/fstab") || strings.Contains(file, "zram") {
		t.Fatalf("file message %q", file)
	}
	both := swapMessage([]string{"/dev/zram0", "/swap.img"})
	if !strings.Contains(both, "zram") || !strings.Contains(both, "/etc/fstab") {
		t.Fatalf("mixed message %q", both)
	}
}

func TestRunUbuntu2604IsSupported(t *testing.T) {
	facts := goodFacts()
	facts.OSVersionID = "26.04"
	results := Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, goodConfig(), []string{"ubuntu-26.04"})
	if Blocked(results) {
		t.Fatalf("unexpected block: %s", Format(results))
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

func TestRunJoinAcceptsAVIPThatAnswersPing(t *testing.T) {
	e := &host.FakeExec{
		Responses: map[string]string{
			"ip -json addr":             "[]",
			"ping -c 1 -W 1 10.0.10.10": "",
		},
	}
	results := RunJoin(context.Background(), e, goodFacts(), nil, goodConfig(), []string{"ubuntu-24.04"})
	if Blocked(results) {
		t.Fatalf("a joining host must not block on the vip of the cluster: %s", Format(results))
	}
	for _, r := range results {
		if r.Name == "vip-free" {
			t.Fatalf("a joining host must not probe the vip: %s", Format(results))
		}
	}
	for _, call := range e.Calls {
		if strings.HasPrefix(call, "ping ") {
			t.Fatalf("a joining host must not ping the vip, ran %q", call)
		}
	}
}

func TestRunJoinKeepsTheOtherChecks(t *testing.T) {
	facts := goodFacts()
	facts.Root = false
	cfg := goodConfig()
	cfg.Spec.API.VIP = "10.0.10.11"
	results := RunJoin(context.Background(), &host.FakeExec{}, facts, nil, cfg, []string{"ubuntu-24.04"})
	failed := map[string]bool{}
	for _, r := range results {
		if !r.OK {
			failed[r.Name] = true
		}
	}
	if !failed["root"] || !failed["vip"] {
		t.Fatalf("want root and vip to fail: %s", Format(results))
	}
}

func TestRunRefusesAKernelThatCannotMountAes256kKeys(t *testing.T) {
	facts := goodFacts()
	facts.Kernel = "6.8.0-45-generic"
	cfg := goodConfig()
	cfg.Spec.Storage.CephxCipher = "aes256k"
	results := Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, cfg, []string{"ubuntu-24.04"})
	if !Blocked(results) || !strings.Contains(Format(results), "kernel 7.0") {
		t.Fatalf("expected a block that names the rule: %s", Format(results))
	}
	if blocked := Blocked(RunJoin(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, cfg, []string{"ubuntu-24.04"})); !blocked {
		t.Fatal("a join must block too")
	}
}

func TestRunAcceptsTheKernelForAes256kAndAnyKernelForAes(t *testing.T) {
	facts := goodFacts()
	facts.Kernel = "7.0.2"
	cfg := goodConfig()
	cfg.Spec.Storage.CephxCipher = "aes256k"
	if Blocked(Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, cfg, []string{"ubuntu-24.04"})) {
		t.Fatal("kernel 7.0 must pass")
	}
	facts.Kernel = "5.14.0"
	cfg.Spec.Storage.CephxCipher = "aes"
	if Blocked(Run(context.Background(), goodExec(), facts, []host.Device{{Path: "/dev/sdb", Block: true}}, cfg, []string{"ubuntu-24.04"})) {
		t.Fatal("aes must pass on an old kernel")
	}
}
