package cephx

import (
	"strings"
	"testing"
)

func TestKernelSupportsAes256kFromSevenZero(t *testing.T) {
	cases := map[string]bool{
		"7.0.0-1-generic":      true,
		"7.2.1":                true,
		"10.1.0":               true,
		"6.8.0-45-generic":     false,
		"5.14.0-427.el9.amd64": false,
		"":                     false,
		"garbage":              false,
	}
	for kernel, want := range cases {
		if got := KernelSupportsAes256k(kernel); got != want {
			t.Errorf("%q: got %v, want %v", kernel, got, want)
		}
	}
}

func TestForKernelPicksTheCipherTheKernelCanMount(t *testing.T) {
	if ForKernel("7.0.1") != Aes256k || ForKernel("6.8.0") != Aes || ForKernel("") != Aes {
		t.Fatal("wrong cipher for the kernel")
	}
}

func TestKernelProblemNamesTheRule(t *testing.T) {
	if KernelProblem("node-1", "7.0.0") != "" {
		t.Fatal("a new kernel has no problem")
	}
	message := KernelProblem("node-1", "6.8.0-45-generic")
	if !strings.Contains(message, "node-1") || !strings.Contains(message, "7.0") || !strings.Contains(message, "aes256k") {
		t.Fatalf("message %q", message)
	}
}
