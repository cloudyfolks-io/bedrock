package operator

import (
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func hostWithKernel(name, kernel string) v1alpha1.Host {
	host := v1alpha1.Host{}
	host.Name = name
	host.Status.Inventory.Firmware.Kernel = kernel
	return host
}

func TestCephxKernelProblemsListEveryHostBelowTheRule(t *testing.T) {
	hosts := []v1alpha1.Host{hostWithKernel("a", "7.0.1"), hostWithKernel("b", "6.8.0-45-generic"), hostWithKernel("c", "")}
	problems := cephxKernelProblems("aes256k", hosts)
	if len(problems) != 2 || !strings.Contains(problems[0], "b") || !strings.Contains(problems[1], "c") || !strings.Contains(problems[0], "kernel 7.0") {
		t.Fatalf("problems %v", problems)
	}
}

func TestCephxKernelProblemsIgnoreTheAesMode(t *testing.T) {
	if problems := cephxKernelProblems("aes", []v1alpha1.Host{hostWithKernel("b", "5.14.0")}); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
	if problems := cephxKernelProblems("", []v1alpha1.Host{hostWithKernel("b", "5.14.0")}); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
}

func TestUpgradePreflightRefusesAnOldKernelInAes256kMode(t *testing.T) {
	in := preflightInput{CephxCipher: "aes256k", Hosts: []v1alpha1.Host{hostWithKernel("b", "6.8.0")}}
	if problem := cephxKernelProblems(in.CephxCipher, in.Hosts); len(problem) == 0 {
		t.Fatal("expected a problem")
	}
	if !strings.Contains(strings.Join(cephxProblems(in), ";"), "b runs kernel") {
		t.Fatal("the preflight check must report the host")
	}
}
