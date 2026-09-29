//go:build e2e

package authn

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type apiServer struct {
	server string
	caFile string
}

type whoamiStatus struct {
	Status struct {
		UserInfo struct {
			Username string   `json:"username"`
			Groups   []string `json:"groups"`
		} `json:"userInfo"`
	} `json:"status"`
}

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func runCommand(name string, env []string, args ...string) commandResult {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func tokenArgs(api apiServer, token string, args ...string) []string {
	return append([]string{"--kubeconfig", os.DevNull, "--server", api.server, "--certificate-authority", api.caFile, "--token", token}, args...)
}

func decodeWhoami(t *testing.T, out string) whoamiStatus {
	t.Helper()
	var status whoamiStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode whoami: %v: %s", err, out)
	}
	return status
}

func kubectlWhoami(t *testing.T, api apiServer, token string) whoamiStatus {
	t.Helper()
	result := runCommand("kubectl", os.Environ(), tokenArgs(api, token, "auth", "whoami", "-o", "json")...)
	if result.err != nil {
		t.Fatalf("kubectl auth whoami: %v: %s", result.err, result.stderr)
	}
	return decodeWhoami(t, result.stdout)
}

func kubectlRefused(api apiServer, token string) (bool, string) {
	result := runCommand("kubectl", os.Environ(), tokenArgs(api, token, "auth", "whoami")...)
	return result.err != nil && strings.Contains(result.stderr, "Unauthorized"), result.stderr
}

func kubectlCanI(t *testing.T, api apiServer, token, verb, resource, namespace string) bool {
	t.Helper()
	result := runCommand("kubectl", os.Environ(), tokenArgs(api, token, "auth", "can-i", verb, resource, "-n", namespace)...)
	switch strings.TrimSpace(result.stdout) {
	case "yes":
		return true
	case "no":
		return false
	}
	t.Fatalf("kubectl auth can-i %s %s -n %s: %v: %s%s", verb, resource, namespace, result.err, result.stdout, result.stderr)
	return false
}

func cliEnv(e env) []string {
	return append(os.Environ(), "HOME="+e.home, "PATH="+e.path)
}

func kubectlWithKubeconfig(t *testing.T, e env, kubeconfig string, args ...string) string {
	t.Helper()
	result := runCommand("kubectl", cliEnv(e), append([]string{"--kubeconfig", kubeconfig}, args...)...)
	if result.err != nil {
		t.Fatalf("kubectl %v: %v: %s", args, result.err, result.stderr)
	}
	return result.stdout
}

func runBedrock(e env, args ...string) commandResult {
	return runCommand(e.bin, cliEnv(e), args...)
}

func bedrockOnPath(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(dir, "bedrock")); err != nil {
		t.Fatalf("link bedrock onto PATH: %v", err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

type bedrockLoginProcess struct {
	code chan string
	done chan commandResult
}

func startBedrockLogin(t *testing.T, e env) *bedrockLoginProcess {
	t.Helper()
	cmd := exec.Command(e.bin, "login", "--server", e.issuer, "--ca-file", e.caFile)
	cmd.Env = cliEnv(e)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bedrock login: %v", err)
	}
	proc := &bedrockLoginProcess{code: make(chan string, 1), done: make(chan commandResult, 1)}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() {
		var lines strings.Builder
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			lines.WriteString(line + "\n")
			if code, found := strings.CutPrefix(line, "code "); found {
				offerCode(proc.code, strings.TrimSpace(code))
			}
		}
		err := cmd.Wait()
		proc.done <- commandResult{stdout: stdout.String(), stderr: lines.String(), err: err}
	}()
	return proc
}

func offerCode(codes chan<- string, code string) {
	select {
	case codes <- code:
	default:
	}
}

func (p *bedrockLoginProcess) userCode(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case code := <-p.code:
		return code
	case result := <-p.done:
		t.Fatalf("bedrock login ended before printing a code: %v: %s", result.err, result.stderr)
	case <-time.After(timeout):
		t.Fatalf("bedrock login: no code within %s", timeout)
	}
	return ""
}

func (p *bedrockLoginProcess) wait(t *testing.T, timeout time.Duration) commandResult {
	t.Helper()
	select {
	case result := <-p.done:
		if result.err != nil {
			t.Fatalf("bedrock login: %v: %s", result.err, result.stderr)
		}
		return result
	case <-time.After(timeout):
		t.Fatalf("bedrock login: no result within %s of the device approval", timeout)
	}
	return commandResult{}
}
