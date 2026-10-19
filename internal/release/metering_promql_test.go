package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const prometheusImageName = "prometheus/prometheus:"

func prometheusImage(images []string) (string, error) {
	for _, image := range images {
		if strings.Contains(image, prometheusImageName) {
			return image, nil
		}
	}
	return "", fmt.Errorf("the image list has no %s image", prometheusImageName)
}

func meteringRuleFile(bundle Bundle) ([]byte, error) {
	rule := objectNamed(groupObjects(bundle, "kube-prometheus-stack"), "PrometheusRule", "bedrock-metering")
	if rule == nil {
		return nil, fmt.Errorf("the built stack has no PrometheusRule bedrock-metering")
	}
	groups, found := rule.Object["spec"].(map[string]any)["groups"]
	if !found {
		return nil, fmt.Errorf("bedrock-metering has no groups")
	}
	return yaml.Marshal(map[string]any{"groups": groups})
}

func promtoolCommand(dir, image string) *exec.Cmd {
	return exec.Command("docker", "run", "--rm",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", dir+":/work:ro", "-w", "/work",
		"--entrypoint", "/bin/promtool",
		image, "test", "rules", "metering.test.yaml")
}

func TestMeteringQueriesPassPromtool(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required to run promtool from the Prometheus image: %v", err)
	}
	bundle, _ := buildStack(t)
	image, err := prometheusImage(bundle.Images)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := meteringRuleFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := os.ReadFile(filepath.Join("testdata", "metering.test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string][]byte{"rules.yaml": rules, "metering.test.yaml": cases} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output, err := promtoolCommand(dir, image).CombinedOutput()
	if err != nil {
		t.Fatalf("promtool %s: %v\n%s", image, err, output)
	}
}
