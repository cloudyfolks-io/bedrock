package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func rbacRelease(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join("..", "release", "testdata", "good")
	if err := os.CopyFS(dir, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	spec := "version: v0.1.0-test\nimage: ghcr.io/cloudyfolks-labs/bedrock:v0.1.0-test\nk0sVersion: v1.36.3+k0s.0\nupgradeFrom: []\ncomponents:\n  - name: bedrock\n    version: v0.1.0-test\n    image: ghcr.io/cloudyfolks-labs/bedrock:v0.1.0-test\n  - name: rook\n    version: v1.20.7\n    image: quay.io/ceph/ceph:v20.2.4\nsupportedOS:\n  - ubuntu-24.04\n"
	if err := os.WriteFile(filepath.Join(dir, "release.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReleaseRBACWritesTheRole(t *testing.T) {
	out := filepath.Join(t.TempDir(), "operator-rbac.yaml")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"release", "rbac", "--release", rbacRelease(t), "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(body, []byte("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n")) || !bytes.Contains(body, []byte("name: bedrock-operator")) {
		t.Fatalf("file:\n%s", body)
	}
	if !strings.HasPrefix(stdout.String(), "wrote "+out+" with ") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestReleaseRBACNeedsARelease(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"release", "rbac", "--release", t.TempDir(), "--out", filepath.Join(t.TempDir(), "x.yaml")}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}
