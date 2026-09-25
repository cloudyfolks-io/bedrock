package cli

import (
	"bytes"
	"slices"
	"testing"
)

func TestReleaseCommandRegistered(t *testing.T) {
	if _, ok := Commands()["release"]; !ok {
		t.Fatal("release command missing")
	}
}

func TestReleaseBuildRequiresFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"release", "build"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("--version")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestReleaseApplyRequiresDir(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"release", "apply"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("--dir")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestReleaseApplyRejectsNonPositiveInterval(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"release", "apply", "--dir", ".", "--interval", "0s"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("--interval")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                 nil,
		"v0.1.0":           {"v0.1.0"},
		"v0.1.0, v0.2.0,,": {"v0.1.0", "v0.2.0"},
	}
	for in, want := range cases {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Fatalf("splitList(%q) = %v, want %v", in, got, want)
		}
	}
}
