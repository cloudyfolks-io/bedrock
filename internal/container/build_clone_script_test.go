package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cloneScriptFor(dir string) string {
	return strings.ReplaceAll(gitCloneScript, workspaceDir, dir)
}

func runCloneScript(t *testing.T, dir, path string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", cloneScriptFor(dir))
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + path, "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCloneScriptChecksOutTheResolvedRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	source := t.TempDir()
	runGit(t, source, "init", "-q")
	must(t, os.WriteFile(filepath.Join(source, "a"), []byte("a"), 0o644))
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-q", "-m", "a")
	first := runGit(t, source, "rev-parse", "HEAD")
	runGit(t, source, "tag", "v1")
	must(t, os.WriteFile(filepath.Join(source, "b"), []byte("b"), 0o644))
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-q", "-m", "b")
	second := runGit(t, source, "rev-parse", "HEAD")
	path := os.Getenv("PATH")
	cases := map[string]string{first: first, "v1": first, "main": second, second: second}
	for revision, want := range cases {
		dir := t.TempDir()
		out, err := runCloneScript(t, dir, path, "GIT_URL="+source, "GIT_REVISION="+revision)
		if err != nil {
			t.Fatalf("%s: %v\n%s", revision, err, out)
		}
		if got := runGit(t, dir, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s: HEAD %s, want %s", revision, got, want)
		}
	}
}

func TestCloneScriptNeverReadsARevisionAsAnOption(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	source := t.TempDir()
	runGit(t, source, "init", "-q")
	must(t, os.WriteFile(filepath.Join(source, "a"), []byte("a"), 0o644))
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-q", "-m", "a")
	for _, revision := range []string{"--pathspec-from-file=/proc/self/environ", "--orphan=x", "-b", "--detach"} {
		dir := t.TempDir()
		out, err := runCloneScript(t, dir, os.Getenv("PATH"), "GIT_URL="+source, "GIT_REVISION="+revision, "GIT_TOKEN=very-secret-token", "GIT_USERNAME=robot")
		if err == nil {
			t.Errorf("%s: the script must fail", revision)
		}
		if strings.Contains(out, "very-secret-token") {
			t.Errorf("%s: the output holds the token:\n%s", revision, out)
		}
	}
}

func TestCloneScriptScopesTheAuthHeaderToTheCloneURL(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "env")
	fake := "#!/bin/sh\nenv | grep '^GIT_CONFIG' | sort >>" + record + "\necho abc123\n"
	must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755))
	path := bin + ":" + os.Getenv("PATH")
	url := "https://git.test/acme/shop.git"
	out, err := runCloneScript(t, t.TempDir(), path, "GIT_URL="+url, "GIT_REVISION=main", "GIT_TOKEN=very-secret-token", "GIT_USERNAME=robot")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	raw, err := os.ReadFile(record)
	must(t, err)
	seen := string(raw)
	if !strings.Contains(seen, "GIT_CONFIG_KEY_1=http."+url+".extraHeader") {
		t.Fatalf("the header must be scoped to the clone URL:\n%s", seen)
	}
	if strings.Contains(seen, "=http.extraHeader") {
		t.Fatalf("a global extraHeader is set:\n%s", seen)
	}
	if !strings.Contains(seen, "GIT_CONFIG_VALUE_1=Authorization: Basic ") {
		t.Fatalf("no basic header:\n%s", seen)
	}
}

func TestCloneScriptWithoutATokenSetsNoHeader(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "env")
	fake := "#!/bin/sh\nenv | grep '^GIT_CONFIG' | sort >>" + record + "\necho abc123\n"
	must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755))
	out, err := runCloneScript(t, t.TempDir(), bin+":"+os.Getenv("PATH"), "GIT_URL=https://git.test/a.git", "GIT_REVISION=main")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	raw, _ := os.ReadFile(record)
	if strings.Contains(string(raw), "extraHeader") {
		t.Fatalf("header set without a token:\n%s", raw)
	}
}
